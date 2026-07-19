package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// QueueRow mirrors a queue row (both the waiting list and on_course state
// live in this one table/status machine). EventID identifies the event this
// row belongs to (queue rows never move between events).
type QueueRow struct {
	ID        int64
	EventID   int64
	DriverID  int64
	VehicleID int64
	Position  float64
	Status    string
	TStartUS  *int64
	PTCount   int
	MCFlag    bool
	CreatedBy *int64
}

const queueSelectCols = `id, event_id, driver_id, vehicle_id, position, status, t_start_us, pt_count, mc_flag, created_by`

func scanQueueRow(row rowScanner) (QueueRow, error) {
	var q QueueRow
	var position sql.NullFloat64
	var tStart sql.NullInt64
	var mc int
	var createdBy sql.NullInt64
	if err := row.Scan(&q.ID, &q.EventID, &q.DriverID, &q.VehicleID, &position, &q.Status, &tStart, &q.PTCount, &mc, &createdBy); err != nil {
		return QueueRow{}, err
	}
	q.Position = position.Float64 // position is only ever NULL for rows this package never creates that way
	if tStart.Valid {
		v := tStart.Int64
		q.TStartUS = &v
	}
	q.MCFlag = mc != 0
	if createdBy.Valid {
		v := createdBy.Int64
		q.CreatedBy = &v
	}
	return q, nil
}

// ListQueue returns eventID's rows with the given status: waiting rows come
// back in position order, everything else (on_course/done/canceled) in id
// order.
func (s *Store) ListQueue(eventID int64, status string) ([]QueueRow, error) {
	orderBy := "id"
	if status == "waiting" {
		orderBy = "position"
	}
	rows, err := s.db.Query(`SELECT `+queueSelectCols+` FROM queue WHERE event_id = ? AND status = ? ORDER BY `+orderBy+`, id`, eventID, status)
	if err != nil {
		return nil, fmt.Errorf("store: list queue: %w", err)
	}
	defer rows.Close()

	var out []QueueRow
	for rows.Next() {
		q, err := scanQueueRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list queue: scan: %w", err)
		}
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list queue: %w", err)
	}
	return out, nil
}

// Enqueue appends (driverID, vehicleID) to the end of eventID's waiting
// queue. Returns ErrAlreadyWaiting (no row inserted) if that combination
// already has a waiting-status row in this event; callers translate that
// into HTTP 409.
func (s *Store) Enqueue(eventID, driverID, vehicleID int64, createdBy *int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue: begin: %w", err)
	}
	defer tx.Rollback()

	var dupCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM queue WHERE event_id = ? AND status = 'waiting' AND driver_id = ? AND vehicle_id = ?`,
		eventID, driverID, vehicleID).Scan(&dupCount); err != nil {
		return 0, fmt.Errorf("store: enqueue: check duplicate: %w", err)
	}
	if dupCount > 0 {
		return 0, ErrAlreadyWaiting
	}

	var maxPos sql.NullFloat64
	if err := tx.QueryRow(`SELECT MAX(position) FROM queue WHERE event_id = ? AND status = 'waiting'`, eventID).Scan(&maxPos); err != nil {
		return 0, fmt.Errorf("store: enqueue: max position: %w", err)
	}
	newPos := 1.0
	if maxPos.Valid {
		newPos = maxPos.Float64 + 1.0
	}

	res, err := tx.Exec(`INSERT INTO queue (event_id, driver_id, vehicle_id, position, status, pt_count, mc_flag, created_by)
		VALUES (?, ?, ?, ?, 'waiting', 0, 0, ?)`, eventID, driverID, vehicleID, newPos, nullableInt64(createdBy))
	if err != nil {
		return 0, fmt.Errorf("store: enqueue: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue: last insert id: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: enqueue: commit: %w", err)
	}
	return id, nil
}

// Reorder sets id's position to the given value, then — within the same
// transaction — looks up id's event_id and checks whether any two adjacent
// waiting rows *of that same event* now sit closer together than 1e-9. If
// so that event's entire waiting queue is renumbered to 1.0, 2.0, 3.0, ...
// to restore room for future fractional inserts. This is expected to be
// rare (repeated fine-grained drag-reorders).
func (s *Store) Reorder(id int64, position float64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: reorder: begin: %w", err)
	}
	defer tx.Rollback()

	var eventID int64
	if err := tx.QueryRow(`SELECT event_id FROM queue WHERE id = ?`, id).Scan(&eventID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("store: reorder: queue row %d not found", id)
		}
		return fmt.Errorf("store: reorder: lookup event_id: %w", err)
	}

	if _, err := tx.Exec(`UPDATE queue SET position = ? WHERE id = ?`, position, id); err != nil {
		return fmt.Errorf("store: reorder: update: %w", err)
	}

	rows, err := tx.Query(`SELECT id, position FROM queue WHERE event_id = ? AND status = 'waiting' ORDER BY position, id`, eventID)
	if err != nil {
		return fmt.Errorf("store: reorder: read positions: %w", err)
	}
	var ids []int64
	var positions []float64
	for rows.Next() {
		var qid int64
		var pos sql.NullFloat64
		if err := rows.Scan(&qid, &pos); err != nil {
			rows.Close()
			return fmt.Errorf("store: reorder: scan position: %w", err)
		}
		ids = append(ids, qid)
		positions = append(positions, pos.Float64)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("store: reorder: rows: %w", err)
	}
	rows.Close()

	needsRenumber := false
	for i := 1; i < len(positions); i++ {
		if positions[i]-positions[i-1] < 1e-9 {
			needsRenumber = true
			break
		}
	}

	if needsRenumber {
		stmt, err := tx.Prepare(`UPDATE queue SET position = ? WHERE id = ?`)
		if err != nil {
			return fmt.Errorf("store: reorder: renumber prepare: %w", err)
		}
		defer stmt.Close()
		for i, qid := range ids {
			if _, err := stmt.Exec(float64(i+1), qid); err != nil {
				return fmt.Errorf("store: reorder: renumber update: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: reorder: commit: %w", err)
	}
	return nil
}

// SetQueueStatus transitions a queue row's status ('waiting' | 'on_course'
// | 'done' | 'canceled'). The state machine itself lives in the web layer;
// this is a bare column write.
func (s *Store) SetQueueStatus(id int64, status string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE queue SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("store: set queue status: %w", err)
	}
	return nil
}

// ClaimQueueRow transitions a queue row's status only if it currently has
// the expected status, as a single compare-and-set UPDATE. claimed=false
// means a concurrent writer moved the row first (e.g. two operators
// launching/adopting the same waiting-queue head at once) - handlers turn
// that lost race into a 409 instead of silently double-writing the row.
func (s *Store) ClaimQueueRow(id int64, from, to string) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE queue SET status = ? WHERE id = ? AND status = ?`, to, id, from)
	if err != nil {
		return false, fmt.Errorf("store: claim queue row: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: claim queue row: %w", err)
	}
	return n > 0, nil
}

// SetStart writes (or, with tStartUS=nil, clears) a queue row's start
// timestamp.
func (s *Store) SetStart(id int64, tStartUS *int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE queue SET t_start_us = ? WHERE id = ?`, nullableInt64(tStartUS), id)
	if err != nil {
		return fmt.Errorf("store: set start: %w", err)
	}
	return nil
}

// SetStartIfUnset stamps t_start_us on a queue row only if it is still
// on_course with no start yet - a compare-and-set both the manual-start
// handler and the sensor-trigger path use, so whichever of them (manual tap
// or sensor pulse) reaches the row first wins and the other cleanly loses
// the race instead of silently overwriting it. The status check also closes
// a narrower race where the row was canceled between being selected as a
// candidate and this write. Returns whether the row was updated.
func (s *Store) SetStartIfUnset(id int64, tStartUS int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.db.Exec(`UPDATE queue SET t_start_us = ? WHERE id = ? AND status = 'on_course' AND t_start_us IS NULL`, tStartUS, id)
	if err != nil {
		return false, fmt.Errorf("store: set start if unset: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: set start if unset: %w", err)
	}
	return n > 0, nil
}

// SetPT adds delta to a queue row's pt_count. If current+delta would be
// negative, the row is left unchanged and (current, ErrPTBelowZero) is
// returned; otherwise the row is updated and (newValue, nil) is returned.
func (s *Store) SetPT(id int64, delta int) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: set pt: begin: %w", err)
	}
	defer tx.Rollback()

	var current int
	if err := tx.QueryRow(`SELECT pt_count FROM queue WHERE id = ?`, id).Scan(&current); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("store: set pt: queue row %d not found", id)
		}
		return 0, fmt.Errorf("store: set pt: %w", err)
	}

	newVal := current + delta
	if newVal < 0 {
		// Guarded: leave the row untouched, report the current value.
		return current, ErrPTBelowZero
	}

	if _, err := tx.Exec(`UPDATE queue SET pt_count = ? WHERE id = ?`, newVal, id); err != nil {
		return 0, fmt.Errorf("store: set pt: update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: set pt: commit: %w", err)
	}
	return newVal, nil
}

// SetMC sets a queue row's mis-course flag.
func (s *Store) SetMC(id int64, on bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE queue SET mc_flag = ? WHERE id = ?`, boolToInt(on), id)
	if err != nil {
		return fmt.Errorf("store: set mc: %w", err)
	}
	return nil
}

// CancelWaiting transitions every 'waiting' row of eventID to 'canceled'.
// Called when closing an event so its queue does not linger in a stale
// waiting state once the event can no longer be launched onto the course;
// on_course/done/canceled rows are left untouched (the caller is
// responsible for rejecting the close while any car is still on_course).
func (s *Store) CancelWaiting(eventID int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE queue SET status = 'canceled' WHERE event_id = ? AND status = 'waiting'`, eventID)
	if err != nil {
		return fmt.Errorf("store: cancel waiting: %w", err)
	}
	return nil
}

// GetQueueRow looks up a single queue row by id.
func (s *Store) GetQueueRow(id int64) (QueueRow, bool, error) {
	row := s.db.QueryRow(`SELECT `+queueSelectCols+` FROM queue WHERE id = ?`, id)
	q, err := scanQueueRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return QueueRow{}, false, nil
	}
	if err != nil {
		return QueueRow{}, false, fmt.Errorf("store: get queue row: %w", err)
	}
	return q, true, nil
}
