package store

import (
	"database/sql"
	"errors"
	"fmt"

	sqlitedriver "modernc.org/sqlite"
)

// setIcon / getIcon back the driver and vehicle icon accessors: both tables
// store a nullable 128x128 JPEG in an `icon` column. table must be a
// compile-time constant ("drivers" / "vehicles") — never user input — since
// it is spliced into the SQL text; what is the per-wrapper error label.
func (s *Store) setIcon(table, what string, id int64, jpeg []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`UPDATE `+table+` SET icon = ? WHERE id = ?`, jpeg, id)
	if err != nil {
		return fmt.Errorf("store: %s: %w", what, err)
	}
	return nil
}

func (s *Store) getIcon(table, what string, id int64) ([]byte, bool, error) {
	var icon []byte
	err := s.db.QueryRow(`SELECT icon FROM `+table+` WHERE id = ?`, id).Scan(&icon)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("store: %s: %w", what, err)
	}
	if icon == nil {
		return nil, false, nil
	}
	return icon, true, nil
}

// ErrAlreadyWaiting is returned by Enqueue when the (driver, vehicle)
// combination already has a row in the waiting queue. Callers (the web
// layer) translate this into an HTTP 409.
var ErrAlreadyWaiting = errors.New("store: driver/vehicle already in waiting queue")

// ErrPTBelowZero is returned by SetPT when applying delta would take
// pt_count below zero. The row is left unchanged and the current
// (unchanged) pt_count is returned alongside this error.
var ErrPTBelowZero = errors.New("store: pt_count cannot go below zero")

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting a single
// scan helper serve QueryRow (single row, ok-bool) and Query (list) call
// sites.
type rowScanner interface {
	Scan(dest ...any) error
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullableInt adapts a possibly-nil *int for use as a database/sql bind
// argument (nil -> SQL NULL).
func nullableInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

// nullableInt64 adapts a possibly-nil *int64 for use as a database/sql bind
// argument (nil -> SQL NULL).
func nullableInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

// sqliteConstraintUnique is SQLite's extended result code for a UNIQUE
// constraint violation (SQLITE_CONSTRAINT_UNIQUE; see
// https://sqlite.org/rescode.html#constraint_unique). modernc.org/sqlite
// only exposes the named constant deep inside its generated modernc.org/
// sqlite/lib bindings, so the numeric code — part of SQLite's stable public
// C API contract — is duplicated here rather than importing that package.
const sqliteConstraintUnique = 2067

// isUniqueConstraintErr reports whether err is a SQLite UNIQUE constraint
// violation, as opposed to any other failure (I/O error, other constraint
// kind, etc.).
func isUniqueConstraintErr(err error) bool {
	var sqliteErr *sqlitedriver.Error
	if errors.As(err, &sqliteErr) {
		return sqliteErr.Code() == sqliteConstraintUnique
	}
	return false
}
