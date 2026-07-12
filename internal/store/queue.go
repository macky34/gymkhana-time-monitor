package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// QueueRow mirrors a queue row (both the waiting list and on_course state
// live in this one table/status machine).
type QueueRow struct {
	ID        int64
	DriverID  int64
	VehicleID int64
	Position  float64
	Status    string
	TStartUS  *int64
	PTCount   int
	MCFlag    bool
	CreatedBy *int64
}

const queueSelectCols = `id, driver_id, vehicle_id, position, status, t_start_us, pt_count, mc_flag, created_by`

func scanQueueRow(row rowScanner) (QueueRow, error) {
	var q QueueRow
	var position sql.NullFloat64
	var tStart sql.NullInt64
	var mc int
	var createdBy sql.NullInt64
	if err := row.Scan(&q.ID, &q.DriverID, &q.VehicleID, &position, &q.Status, &tStart, &q.PTCount, &mc, &createdBy); err != nil {
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

// ListQueue returns rows with the given status: waiting rows come back in
// position order, everything else (on_course/done/canceled) in id order.
func (s *Store) ListQueue(status string) ([]QueueRow, error) {
	orderBy := "id"
	if status == "waiting" {
		orderBy = "position"
	}
	rows, err := s.db.Query(`SELECT `+queueSelectCols+` FROM queue WHERE status = ? ORDER BY `+orderBy+`, id`, status)
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

// Enqueue appends (driverID, vehicleID) to the end of the waiting queue.
// Returns ErrAlreadyWaiting (no row inserted) if that combination already
// has a waiting-status row; callers translate that into HTTP 409.
func (s *Store) Enqueue(driverID, vehicleID int64, createdBy *int64) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("store: enqueue: begin: %w", err)
	}
	defer tx.Rollback()

	var dupCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM queue WHERE status = 'waiting' AND driver_id = ? AND vehicle_id = ?`,
		driverID, vehicleID).Scan(&dupCount); err != nil {
		return 0, fmt.Errorf("store: enqueue: check duplicate: %w", err)
	}
	if dupCount > 0 {
		return 0, ErrAlreadyWaiting
	}

	var maxPos sql.NullFloat64
	if err := tx.QueryRow(`SELECT MAX(position) FROM queue WHERE status = 'waiting'`).Scan(&maxPos); err != nil {
		return 0, fmt.Errorf("store: enqueue: max position: %w", err)
	}
	newPos := 1.0
	if maxPos.Valid {
		newPos = maxPos.Float64 + 1.0
	}

	res, err := tx.Exec(`INSERT INTO queue (driver_id, vehicle_id, position, status, pt_count, mc_flag, created_by)
		VALUES (?, ?, ?, 'waiting', 0, 0, ?)`, driverID, vehicleID, newPos, nullableInt64(createdBy))
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
// transaction — checks whether any two adjacent waiting rows now sit closer
// together than 1e-9. If so the entire waiting queue is renumbered to
// 1.0, 2.0, 3.0, ... to restore room for future fractional inserts. This is
// expected to be rare (repeated fine-grained drag-reorders).
func (s *Store) Reorder(id int64, position float64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: reorder: begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE queue SET position = ? WHERE id = ?`, position, id); err != nil {
		return fmt.Errorf("store: reorder: update: %w", err)
	}

	rows, err := tx.Query(`SELECT id, position FROM queue WHERE status = 'waiting' ORDER BY position, id`)
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
