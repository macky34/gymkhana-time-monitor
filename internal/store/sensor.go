package store

import "fmt"

// InsertSensorEvent records one raw sensor trigger. Returns (false, nil)
// without error when (sensorID, bootID, seq) has already been recorded —
// this is how the 3x UDP resend (plan/DESIGN.md §7.1) gets deduplicated;
// the caller only proceeds to pairing logic on the first occurrence.
func (s *Store) InsertSensorEvent(sensorID string, bootID, seq int64, tsUS int64, receivedAt int64) (bool, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.Exec(`INSERT INTO sensor_events (sensor_id, boot_id, seq, timestamp_us, received_at)
		VALUES (?, ?, ?, ?, ?)`, sensorID, bootID, seq, tsUS, receivedAt)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return false, nil
		}
		return false, fmt.Errorf("store: insert sensor event: %w", err)
	}
	return true, nil
}

// AppendAudit records one administrative action for the audit log.
func (s *Store) AppendAudit(atMS int64, driverID *int64, action, detailJSON string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.Exec(`INSERT INTO audit (at_ms, driver_id, action, detail) VALUES (?, ?, ?, ?)`,
		atMS, nullableInt64(driverID), action, detailJSON)
	if err != nil {
		return fmt.Errorf("store: append audit: %w", err)
	}
	return nil
}

// VacuumInto writes a consistent snapshot of the whole database to path via
// SQLite's own VACUUM INTO, using a bound parameter (never string-concatenated
// SQL) so the destination — which may be an arbitrary, caller-supplied,
// Windows-or-POSIX filesystem path — can never be misinterpreted as SQL.
// Creating the parent directory (e.g. ./snapshots/) is the caller's
// responsibility; SQLite (like any file API) fails if it doesn't exist.
// VACUUM INTO also fails if the destination file already exists.
func (s *Store) VacuumInto(path string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.Exec(`VACUUM INTO ?`, path)
	if err != nil {
		return fmt.Errorf("store: vacuum into %s: %w", path, err)
	}
	return nil
}
