package store

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// migration is one ordered, idempotent schema step applied to an existing
// database that predates it. Steps only ever move forward — there is no
// down migration; rolling back means restoring the pre-migration backup
// (see applyMigrations) and running the old binary again (Server-Setup
// wiki, アップグレード手順).
//
// apply must be safe to run against a database that may already be at (or
// past) version — see the package doc on Open for why idempotency matters
// even though each step also only ever runs once per database in practice.
type migration struct {
	version int    // PRAGMA user_version after this step succeeds
	name    string // short label, used in log lines and the backup filename
	apply   func(*sql.Tx) error
}

// migrations is the ordered list of schema steps beyond the shape baked
// into schemaSQL. Empty for now — schemaSQL already describes the current
// latest shape, so there is nothing to migrate yet. The first entry lands
// with the feature that needs it (e.g. a new column on an existing table);
// see applyMigrations and addColumnIfMissing for how such a step is
// written.
var migrations = []migration{}

// schemaVersion reads the database's current schema version from SQLite's
// own user_version pragma (a plain integer slot SQLite reserves for
// application use — no extra table needed).
func schemaVersion(db *sql.DB) (int, error) {
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}

// setSchemaVersion sets user_version directly (outside any transaction) —
// used once, right after a brand-new database's tables are created by
// schemaSQL, to record that it already starts at the latest version.
func setSchemaVersion(db *sql.DB, v int) error {
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		return fmt.Errorf("store: set schema version: %w", err)
	}
	return nil
}

// setSchemaVersionTx is setSchemaVersion's transactional counterpart, used
// inside applyMigrations so a step's DDL and its version bump commit or
// roll back together.
func setSchemaVersionTx(tx *sql.Tx, v int) error {
	if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v)); err != nil {
		return fmt.Errorf("store: set schema version: %w", err)
	}
	return nil
}

// tableExists reports whether a table by that name already exists, via
// sqlite_master. Used by Open to tell a brand-new database file apart from
// an existing one *before* schemaSQL's CREATE TABLE IF NOT EXISTS runs and
// makes every table present regardless.
func tableExists(db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("store: check table %s: %w", name, err)
	}
	return n > 0, nil
}

// identRe restricts identifiers accepted by addColumnIfMissing. SQLite has
// no way to bind a table/column name as a query parameter (PRAGMA and DDL
// only accept them inlined into the statement text), so every call site
// that builds SQL this way validates through this pattern first — even
// though today's callers only ever pass compile-time string constants, not
// anything derived from user input.
var identRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// addColumnIfMissing adds column to table with the given DDL fragment
// (e.g. "INTEGER NOT NULL DEFAULT 0"), unless the column is already
// present. This is what makes a migration step safe to describe once and
// have it double as both "upgrade an old database" and "no-op if somehow
// re-run" — schemaSQL's CREATE TABLE IF NOT EXISTS gives that same
// idempotency for whole tables; this gives it for columns added to a
// table that already existed.
func addColumnIfMissing(tx *sql.Tx, table, column, ddl string) error {
	if !identRe.MatchString(table) || !identRe.MatchString(column) {
		return fmt.Errorf("store: invalid identifier: table=%q column=%q", table, column)
	}

	rows, err := tx.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return fmt.Errorf("store: table_info(%s): %w", table, err)
	}
	found := false
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return fmt.Errorf("store: table_info(%s): scan: %w", table, err)
		}
		if name == column {
			found = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: table_info(%s): %w", table, err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: table_info(%s): %w", table, err)
	}
	if found {
		return nil
	}

	if _, err := tx.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, ddl)); err != nil {
		return fmt.Errorf("store: alter table %s add column %s: %w", table, column, err)
	}
	return nil
}

// applyMigrations brings an existing database from its current
// PRAGMA user_version up to the latest version described by migs, in
// order. Called only for databases that already existed before this Open
// call (see Open) — a brand-new database gets schemaSQL's latest shape
// directly and has its version recorded by setSchemaVersion instead.
//
// If there is nothing to do (the database is already current), this is a
// no-op: no backup is taken and no write happens. Otherwise, before
// applying anything, it takes a full VACUUM INTO backup under backupDir —
// if that backup fails, migration is aborted and Open reports an error
// without having touched the database (see the comment on the backup
// step below for why a failed backup is treated as fatal rather than
// skipped). Each migration step then runs in its own transaction together
// with the user_version bump, so a step that fails partway rolls back
// cleanly and the version does not advance past the last one that
// actually committed.
func applyMigrations(db *sql.DB, migs []migration, backupDir string) error {
	from, err := schemaVersion(db)
	if err != nil {
		return err
	}

	var pending []migration
	for _, m := range migs {
		if m.version > from {
			pending = append(pending, m)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	// Take a pre-migration snapshot before touching anything. A failed
	// backup is treated as fatal (Open returns an error and the server does
	// not start) rather than skipped, because the most likely cause — no
	// disk space, a permissions problem — is exactly the condition under
	// which running ALTER TABLE against the live database is riskiest.
	// Fixing the underlying cause and restarting picks this back up from
	// the same, still-unmodified, database.
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return fmt.Errorf("store: pre-migration backup dir %s: %w", backupDir, err)
	}
	backupPath := filepath.Join(backupDir, fmt.Sprintf("premigrate-%d-v%d.sqlite3", time.Now().Unix(), from))
	if _, err := db.Exec(`VACUUM INTO ?`, backupPath); err != nil {
		return fmt.Errorf("store: pre-migration backup %s: %w (fix the underlying issue — e.g. free disk space — and restart; the database has not been modified)", backupPath, err)
	}

	for _, m := range pending {
		if err := applyOne(db, m); err != nil {
			return fmt.Errorf("store: migration %d (%s): %w", m.version, m.name, err)
		}
		log.Printf("store: migrated to version %d (%s)", m.version, m.name)
	}
	return nil
}

// applyOne runs a single migration step's DDL and its user_version bump in
// one transaction, so both commit together or neither does.
func applyOne(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() // no-op once Commit has succeeded

	if err := m.apply(tx); err != nil {
		return err
	}
	if err := setSchemaVersionTx(tx, m.version); err != nil {
		return err
	}
	return tx.Commit()
}
