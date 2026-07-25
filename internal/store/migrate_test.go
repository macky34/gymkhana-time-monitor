package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openScratchDB opens a bare SQLite file at dir/name.sqlite3 with the same
// pragmas Store.Open uses, but without applying schemaSQL or any
// migration — tests build whatever table shape they need directly, to
// stand in for "a database that predates today's schemaSQL/migrations".
func openScratchDB(t *testing.T, dir, name string) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, name)
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open scratch db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping scratch db: %v", err)
	}
	return db
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	return false
}

// --- Open: new database ------------------------------------------------

func TestOpenNewDatabaseRecordsLatestSchemaVersionWithoutMigrating(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event.sqlite3")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()

	v, err := schemaVersion(st.db)
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if v != len(migrations) {
		t.Errorf("schema version = %d, want %d (len(migrations))", v, len(migrations))
	}

	// A brand-new database has nothing to migrate, so no pre-migration
	// backup should have been taken.
	matches, err := filepath.Glob(filepath.Join(dir, "snapshots", "premigrate-*"))
	if err != nil {
		t.Fatalf("glob snapshots: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("unexpected pre-migration backups for a new database: %v", matches)
	}
}

// --- applyMigrations: upgrading an existing database --------------------

func TestApplyMigrationsUpgradesExistingDatabaseAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	db := openScratchDB(t, dir, "old.sqlite3")

	if _, err := db.Exec(`CREATE TABLE t1 (a INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create t1: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO t1 (a) VALUES (1)`); err != nil {
		t.Fatalf("seed t1: %v", err)
	}

	migs := []migration{
		{version: 1, name: "t1-add-b", apply: func(tx *sql.Tx) error {
			return addColumnIfMissing(tx, "t1", "b", "INTEGER NOT NULL DEFAULT 0")
		}},
	}
	backupDir := filepath.Join(dir, "snapshots")

	if err := applyMigrations(db, migs, backupDir); err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}

	if v, err := schemaVersion(db); err != nil || v != 1 {
		t.Fatalf("schemaVersion after migrate = %d, %v; want 1, nil", v, err)
	}
	if !hasColumn(t, db, "t1", "b") {
		t.Error("column b was not added to t1")
	}

	before, err := filepath.Glob(filepath.Join(backupDir, "premigrate-*-v0.sqlite3"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(before) != 1 {
		t.Fatalf("pre-migration backups = %v, want exactly 1", before)
	}

	// Re-running with the same list: the database is already at version 1,
	// so nothing is pending — no new backup, no error, version unchanged.
	if err := applyMigrations(db, migs, backupDir); err != nil {
		t.Fatalf("applyMigrations (idempotent re-run): %v", err)
	}
	if v, err := schemaVersion(db); err != nil || v != 1 {
		t.Fatalf("schemaVersion after re-run = %d, %v; want 1, nil", v, err)
	}
	after, err := filepath.Glob(filepath.Join(backupDir, "premigrate-*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(after) != 1 {
		t.Errorf("pre-migration backups after no-op re-run = %v, want still exactly 1 (no new backup)", after)
	}
}

func TestApplyMigrationsNoOpWhenNothingPending(t *testing.T) {
	dir := t.TempDir()
	db := openScratchDB(t, dir, "current.sqlite3")
	backupDir := filepath.Join(dir, "snapshots")

	if err := applyMigrations(db, nil, backupDir); err != nil {
		t.Fatalf("applyMigrations with no migrations: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(backupDir, "premigrate-*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(matches) != 0 {
		t.Errorf("unexpected backups when nothing was pending: %v", matches)
	}
}

// --- addColumnIfMissing --------------------------------------------------

func TestAddColumnIfMissingIsNoOpWhenColumnAlreadyExists(t *testing.T) {
	dir := t.TempDir()
	db := openScratchDB(t, dir, "t.sqlite3")
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create t: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	// If the "already present" guard were missing, this would fail with a
	// "duplicate column name" error from SQLite.
	if err := addColumnIfMissing(tx, "t", "x", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		t.Fatalf("addColumnIfMissing on existing column: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestAddColumnIfMissingRejectsInvalidIdentifiers(t *testing.T) {
	dir := t.TempDir()
	db := openScratchDB(t, dir, "t.sqlite3")
	if _, err := db.Exec(`CREATE TABLE t (x INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create t: %v", err)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	if err := addColumnIfMissing(tx, "t; DROP TABLE t", "y", "TEXT"); err == nil {
		t.Error("expected error for invalid table identifier, got nil")
	}
	if err := addColumnIfMissing(tx, "t", "y; DROP TABLE t", "TEXT"); err == nil {
		t.Error("expected error for invalid column identifier, got nil")
	}
}

// --- applyMigrations: a failing step rolls back and halts ---------------

func TestApplyMigrationsRollsBackFailingStepAndDoesNotRunLaterSteps(t *testing.T) {
	dir := t.TempDir()
	db := openScratchDB(t, dir, "old.sqlite3")
	if _, err := db.Exec(`CREATE TABLE t1 (a INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create t1: %v", err)
	}

	migs := []migration{
		{version: 1, name: "partial-then-fails", apply: func(tx *sql.Tx) error {
			// This half succeeds on its own...
			if err := addColumnIfMissing(tx, "t1", "b", "INTEGER NOT NULL DEFAULT 0"); err != nil {
				return err
			}
			// ...but the step as a whole fails here, so both halves must
			// roll back together (single Tx per step).
			return addColumnIfMissing(tx, "not an identifier", "c", "TEXT")
		}},
		{version: 2, name: "never-runs", apply: func(tx *sql.Tx) error {
			return addColumnIfMissing(tx, "t1", "d", "INTEGER NOT NULL DEFAULT 0")
		}},
	}
	backupDir := filepath.Join(dir, "snapshots")

	if err := applyMigrations(db, migs, backupDir); err == nil {
		t.Fatal("expected applyMigrations to fail, got nil")
	}

	if v, err := schemaVersion(db); err != nil || v != 0 {
		t.Fatalf("schemaVersion after failed migration = %d, %v; want 0, nil (unchanged)", v, err)
	}
	if hasColumn(t, db, "t1", "b") {
		t.Error("column b should have rolled back along with the rest of its failing step")
	}
	if hasColumn(t, db, "t1", "d") {
		t.Error("later step (version 2) must not run after an earlier step fails")
	}

	// A backup should still have been taken before the (failed) attempt —
	// pending work existed at the time, the DDL just didn't succeed.
	matches, err := filepath.Glob(filepath.Join(backupDir, "premigrate-*-v0.sqlite3"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("pre-migration backups = %v, want exactly 1", matches)
	}
}
