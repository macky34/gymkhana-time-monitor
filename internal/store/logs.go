package store

import (
	"database/sql"
	"errors"
	"fmt"

	"timemon/internal/domain"
)

// LogRow mirrors a logs row. DriverID/VehicleID are nil for an unassigned
// log (a raw sensor trigger pairing that couldn't be matched to a queue
// entry).
type LogRow struct {
	ID          int64
	DriverID    *int64
	VehicleID   *int64
	RawMS       int
	PTCount     int
	IsMC        bool
	TimestampMS int64
	Source      string
	EditedAt    *int64
	IsDeleted   bool
}

const logSelectCols = `id, driver_id, vehicle_id, raw_ms, pt_count, is_mc, timestamp_ms, source, edited_at, is_deleted`

func scanLogRow(row rowScanner) (LogRow, error) {
	var l LogRow
	var driverID, vehicleID, editedAt sql.NullInt64
	var isMC, isDeleted int
	if err := row.Scan(&l.ID, &driverID, &vehicleID, &l.RawMS, &l.PTCount, &isMC, &l.TimestampMS,
		&l.Source, &editedAt, &isDeleted); err != nil {
		return LogRow{}, err
	}
	if driverID.Valid {
		v := driverID.Int64
		l.DriverID = &v
	}
	if vehicleID.Valid {
		v := vehicleID.Int64
		l.VehicleID = &v
	}
	l.IsMC = isMC != 0
	if editedAt.Valid {
		v := editedAt.Int64
		l.EditedAt = &v
	}
	l.IsDeleted = isDeleted != 0
	return l, nil
}

// InsertLog inserts a log row (sensor pairing, manual timing, or admin
// manual entry) and returns its id.
func (s *Store) InsertLog(l LogRow) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	res, err := s.db.Exec(`INSERT INTO logs (driver_id, vehicle_id, raw_ms, pt_count, is_mc, timestamp_ms, source, edited_at, is_deleted)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableInt64(l.DriverID), nullableInt64(l.VehicleID), l.RawMS, l.PTCount, boolToInt(l.IsMC),
		l.TimestampMS, l.Source, nullableInt64(l.EditedAt), boolToInt(l.IsDeleted))
	if err != nil {
		return 0, fmt.Errorf("store: insert log: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: insert log: last insert id: %w", err)
	}
	return id, nil
}

// UpdateLog overwrites a log row in full (l.ID selects the row); the caller
// sets EditedAt itself.
func (s *Store) UpdateLog(l LogRow) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.Exec(`UPDATE logs SET driver_id=?, vehicle_id=?, raw_ms=?, pt_count=?, is_mc=?, timestamp_ms=?,
		source=?, edited_at=?, is_deleted=? WHERE id=?`,
		nullableInt64(l.DriverID), nullableInt64(l.VehicleID), l.RawMS, l.PTCount, boolToInt(l.IsMC),
		l.TimestampMS, l.Source, nullableInt64(l.EditedAt), boolToInt(l.IsDeleted), l.ID)
	if err != nil {
		return fmt.Errorf("store: update log: %w", err)
	}
	return nil
}

// SoftDeleteLog sets is_deleted=1 on a log row (admin deletion — recomputes
// rankings/heat numbers without losing the audit trail).
func (s *Store) SoftDeleteLog(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE logs SET is_deleted = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: soft delete log: %w", err)
	}
	return nil
}

// HardDeleteLog physically removes a log row. This exists only for the
// undo-goal flow (plan/DESIGN.md §7.3): while still within the finish
// grace period, undoing a finish removes the log that was already written
// and restores the queue row to on_course/running.
func (s *Store) HardDeleteLog(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`DELETE FROM logs WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: hard delete log: %w", err)
	}
	return nil
}

// GetLog looks up a single log row by id, regardless of is_deleted.
func (s *Store) GetLog(id int64) (LogRow, bool, error) {
	row := s.db.QueryRow(`SELECT `+logSelectCols+` FROM logs WHERE id = ?`, id)
	l, err := scanLogRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return LogRow{}, false, nil
	}
	if err != nil {
		return LogRow{}, false, fmt.Errorf("store: get log: %w", err)
	}
	return l, true, nil
}

// ListLogs returns a page of all logs (including deleted and unassigned
// ones — this feeds the admin log-management table), newest first, plus
// the total row count for pagination.
func (s *Store) ListLogs(limit, offset int) ([]LogRow, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: list logs: count: %w", err)
	}

	rows, err := s.db.Query(`SELECT `+logSelectCols+` FROM logs ORDER BY timestamp_ms DESC, id DESC LIMIT ? OFFSET ?`,
		limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("store: list logs: %w", err)
	}
	defer rows.Close()

	var out []LogRow
	for rows.Next() {
		l, err := scanLogRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: list logs: scan: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: list logs: %w", err)
	}
	return out, total, nil
}

func scanRunRows(rows *sql.Rows) ([]domain.RunRow, error) {
	var out []domain.RunRow
	for rows.Next() {
		var r domain.RunRow
		var driverID, vehicleID int64
		var isMC int
		if err := rows.Scan(&r.LogID, &driverID, &vehicleID, &r.RawMS, &r.PTCount, &isMC, &r.TimestampMS); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		r.Combo = domain.ComboKey{DriverID: driverID, VehicleID: vehicleID}
		r.IsMC = isMC != 0
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ListRuns returns every non-deleted, fully-assigned log as a
// domain.RunRow — the input to domain.Rank. Ordered by timestamp_ms, id to
// match the heat-numbering order (plan/DESIGN.md §4.4).
func (s *Store) ListRuns() ([]domain.RunRow, error) {
	rows, err := s.db.Query(`SELECT id, driver_id, vehicle_id, raw_ms, pt_count, is_mc, timestamp_ms
		FROM logs
		WHERE is_deleted = 0 AND driver_id IS NOT NULL AND vehicle_id IS NOT NULL
		ORDER BY timestamp_ms, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()
	out, err := scanRunRows(rows)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	return out, nil
}

// ListRunsByCombo is ListRuns scoped to one (driver, vehicle) combination
// (drives the combination drilldown / heat numbering for that pair alone).
func (s *Store) ListRunsByCombo(d, v int64) ([]domain.RunRow, error) {
	rows, err := s.db.Query(`SELECT id, driver_id, vehicle_id, raw_ms, pt_count, is_mc, timestamp_ms
		FROM logs
		WHERE is_deleted = 0 AND driver_id = ? AND vehicle_id = ?
		ORDER BY timestamp_ms, id`, d, v)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by combo: %w", err)
	}
	defer rows.Close()
	out, err := scanRunRows(rows)
	if err != nil {
		return nil, fmt.Errorf("store: list runs by combo: %w", err)
	}
	return out, nil
}

// ListUnassignedLogs returns non-deleted logs missing a driver and/or
// vehicle (orphaned sensor pairings awaiting admin assignment), oldest
// first.
func (s *Store) ListUnassignedLogs() ([]LogRow, error) {
	rows, err := s.db.Query(`SELECT ` + logSelectCols + ` FROM logs
		WHERE is_deleted = 0 AND (driver_id IS NULL OR vehicle_id IS NULL)
		ORDER BY timestamp_ms, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list unassigned logs: %w", err)
	}
	defer rows.Close()

	var out []LogRow
	for rows.Next() {
		l, err := scanLogRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list unassigned logs: scan: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list unassigned logs: %w", err)
	}
	return out, nil
}
