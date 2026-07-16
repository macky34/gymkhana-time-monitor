// Package store is the sole writer of the timemon event SQLite database.
//
// Concurrency model: all mutating methods acquire Store.writeMu before
// touching the database, serializing every write (see the
// Architecture wiki page). Read-only methods do not take writeMu and may run
// concurrently with each other and with an in-flight write transaction —
// SQLite's WAL journal mode (enabled below) lets readers see the last
// committed snapshot without blocking on the writer. The connection pool is
// kept intentionally small (see Open) since this serves a single event's
// worth of traffic (a handful of browser clients), not a high-concurrency
// web service.
package store

import (
	"database/sql"
	"fmt"
	"sync"

	_ "modernc.org/sqlite"
)

// Store wraps the event database handle.
type Store struct {
	db      *sql.DB
	writeMu sync.Mutex
}

// Open opens (creating the file if necessary) the SQLite database at path
// and ensures the schema exists. Every physical connection the pool opens
// gets journal_mode=WAL, busy_timeout=5000 and foreign_keys=ON via DSN
// pragma parameters — this matters because foreign_keys in particular is a
// per-connection setting that SQLite does not persist, so it must be
// re-applied for every new connection the pool creates, not just once after
// Open (see modernc.org/sqlite's _pragma DSN parameter; verified against
// Windows-style absolute paths containing a drive-letter colon and spaces,
// which work fine because the driver splits the DSN on the first literal
// '?' rather than doing full RFC 3986 URI parsing).
func Open(path string) (*Store, error) {
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	// Small, bounded pool: writes are already serialized above the DB layer
	// by writeMu, so this only bounds concurrent readers/idle file handles.
	db.SetMaxOpenConns(8)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: create schema: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}
