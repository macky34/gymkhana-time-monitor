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

// TestMigrationsVersionsAreSequential guards the invariant Open relies on:
// a brand-new database is stamped with len(migrations) directly (see Open's
// doc comment), which only agrees with what an existing database reaches by
// actually applying every step if migrations[i].version == i+1 for every i —
// no gaps, no reordering, no duplicates.
func TestMigrationsVersionsAreSequential(t *testing.T) {
	for i, m := range migrations {
		if want := i + 1; m.version != want {
			t.Errorf("migrations[%d] (%s) has version %d, want %d", i, m.name, m.version, want)
		}
	}
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

// --- the real, production `migrations` list, end to end -----------------

// TestRealMigrationsAddIconRevColumns exercises the actual migrations var
// (not a synthetic list) through the real Open() code path, standing in for
// an event.sqlite3 that predates icon_rev: rather than hand-writing a copy
// of the old schemaSQL (which would only ever test itself, and rot the
// moment schemaSQL changes again), it takes a database already at the
// latest shape and surgically removes just the column version 1 adds -
// exactly the delta an old database actually has relative to today's code.
func TestRealMigrationsAddIconRevColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event.sqlite3")

	// 1. Build a latest-shape database through the real, public API and seed
	// some data on it - including icons, so the migration's backfill has
	// something to prove. Using SetIcon/SetVehicleIcon here (rather than
	// writing to the icon column by hand) doesn't matter for what happens
	// next: their icon_rev side effect is about to be erased in step 2
	// regardless of what value it left behind.
	st1, err := Open(path)
	if err != nil {
		t.Fatalf("Open (build latest-shape db): %v", err)
	}
	seedMinimal(t, st1)
	driverClasses, err := st1.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: ok=%v err=%v", len(driverClasses) > 0, err)
	}
	dtClasses, err := st1.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("ListClassDefs drivetrain: ok=%v err=%v", len(dtClasses) > 0, err)
	}
	driverID, err := st1.CreateDriver("移行対象", driverClasses[0].ID, "tok-migrate", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	// A driver with no icon at all: the migration's backfill only touches
	// icon IS NOT NULL rows, so this one must stay at rev 0 (not get swept
	// up by the same backfill that gives the icon-bearing driver a rev).
	noIconDriverID, err := st1.CreateDriver("アイコン無し", driverClasses[0].ID, "tok-no-icon", "user")
	if err != nil {
		t.Fatalf("CreateDriver (no icon): %v", err)
	}
	vehicleID, err := st1.CreateVehicle(Vehicle{Number: 1, Name: "V", Engine: "gasoline", DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}
	if err := st1.AddEntry(driverID, vehicleID); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	driverIcon := []byte{0xAA, 0xBB}
	vehicleIcon := []byte{0xCC, 0xDD}
	if err := st1.SetIcon(driverID, driverIcon); err != nil {
		t.Fatalf("SetIcon: %v", err)
	}
	if err := st1.SetVehicleIcon(vehicleID, vehicleIcon); err != nil {
		t.Fatalf("SetVehicleIcon: %v", err)
	}

	// 2. Revert to "before version 1": drop icon_rev (SQLite has supported
	// DROP COLUMN since 3.35) and roll user_version back to 0. This is the
	// only structural difference an actual pre-migration event.sqlite3 has
	// relative to what schemaSQL produces today.
	for _, tbl := range []string{"drivers", "vehicles"} {
		if _, err := st1.db.Exec(`ALTER TABLE ` + tbl + ` DROP COLUMN icon_rev`); err != nil {
			t.Fatalf("drop icon_rev from %s: %v", tbl, err)
		}
	}
	if err := setSchemaVersion(st1.db, 0); err != nil {
		t.Fatalf("reset schema version: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 3. Re-open through the real public API. This is the actual code path
	// production hits on every restart: Open sees an existing database
	// (tableExists("events")), applies schemaSQL (a no-op for existing
	// tables), then applyMigrations(db, migrations, ...) with the real,
	// package-level migrations - not a synthetic list.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("Open (apply real migrations): %v", err)
	}
	defer st2.Close()

	if v, err := schemaVersion(st2.db); err != nil || v != len(migrations) {
		t.Fatalf("schemaVersion after migrate = %d, %v; want %d, nil", v, err, len(migrations))
	}

	backupDir := filepath.Join(dir, "snapshots")
	matches, err := filepath.Glob(filepath.Join(backupDir, "premigrate-*-v0.sqlite3"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("pre-migration backups = %v, want exactly 1", matches)
	}

	// 4. Data preservation + backfill: an icon uploaded before this column
	// existed must survive, and start at rev 1 (not the column's own
	// DEFAULT 0) - see the migration's backfill step and the invariant it
	// establishes (icon_rev > 0 iff icon IS NOT NULL) in migrate.go.
	gotDriverIcon, ok, err := st2.GetIcon(driverID)
	if err != nil || !ok || string(gotDriverIcon) != string(driverIcon) {
		t.Fatalf("GetIcon after migrate: ok=%v err=%v got=%v want=%v", ok, err, gotDriverIcon, driverIcon)
	}
	gotVehicleIcon, ok, err := st2.GetVehicleIcon(vehicleID)
	if err != nil || !ok || string(gotVehicleIcon) != string(vehicleIcon) {
		t.Fatalf("GetVehicleIcon after migrate: ok=%v err=%v got=%v want=%v", ok, err, gotVehicleIcon, vehicleIcon)
	}

	// 5. The real regression this migration risks: every Go scan path that
	// touches drivers/vehicles must still line up column-for-column,
	// including the two hand-written JOINs (ListEntriesByDriver /
	// ListDriversByVehicle) that don't go through the shared
	// driverSelectCols/vehicleSelectCols constants by accident rather than
	// by construction.
	d, ok, err := st2.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver after migrate: ok=%v err=%v", ok, err)
	}
	if !d.HasIcon || d.IconRev <= 0 {
		t.Errorf("GetDriver after migrate: HasIcon=%v IconRev=%d, want true, >0", d.HasIcon, d.IconRev)
	}
	noIconDriver, ok, err := st2.GetDriver(noIconDriverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver (no icon) after migrate: ok=%v err=%v", ok, err)
	}
	if noIconDriver.HasIcon || noIconDriver.IconRev != 0 {
		t.Errorf("GetDriver (no icon) after migrate: HasIcon=%v IconRev=%d, want false, 0 (backfill must not touch icon-less rows)", noIconDriver.HasIcon, noIconDriver.IconRev)
	}
	if _, err := st2.ListDrivers(); err != nil {
		t.Errorf("ListDrivers after migrate: %v", err)
	}
	v, ok, err := st2.GetVehicle(vehicleID)
	if err != nil || !ok {
		t.Fatalf("GetVehicle after migrate: ok=%v err=%v", ok, err)
	}
	if !v.HasIcon || v.IconRev <= 0 {
		t.Errorf("GetVehicle after migrate: HasIcon=%v IconRev=%d, want true, >0", v.HasIcon, v.IconRev)
	}
	if _, err := st2.ListVehicles(); err != nil {
		t.Errorf("ListVehicles after migrate: %v", err)
	}
	vehiclesForDriver, err := st2.ListEntriesByDriver(driverID)
	if err != nil {
		t.Fatalf("ListEntriesByDriver after migrate: %v", err)
	}
	if len(vehiclesForDriver) != 1 || vehiclesForDriver[0].ID != vehicleID || vehiclesForDriver[0].IconRev <= 0 {
		t.Errorf("ListEntriesByDriver after migrate = %+v, want one vehicle %d with IconRev>0", vehiclesForDriver, vehicleID)
	}
	driversForVehicle, err := st2.ListDriversByVehicle(vehicleID)
	if err != nil {
		t.Fatalf("ListDriversByVehicle after migrate: %v", err)
	}
	if len(driversForVehicle) != 1 || driversForVehicle[0].ID != driverID || driversForVehicle[0].IconRev <= 0 {
		t.Errorf("ListDriversByVehicle after migrate = %+v, want one driver %d with IconRev>0", driversForVehicle, driverID)
	}

	// 6. Idempotency of the real list against an already-migrated database:
	// re-running Open must not error and must not take another backup.
	if err := st2.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	st3, err := Open(path)
	if err != nil {
		t.Fatalf("Open (idempotent re-run): %v", err)
	}
	defer st3.Close()
	if v, err := schemaVersion(st3.db); err != nil || v != len(migrations) {
		t.Fatalf("schemaVersion after re-open = %d, %v; want %d, nil", v, err, len(migrations))
	}
	matchesAfter, err := filepath.Glob(filepath.Join(backupDir, "premigrate-*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(matchesAfter) != 1 {
		t.Errorf("pre-migration backups after idempotent re-open = %v, want still exactly 1 (no new backup)", matchesAfter)
	}
}
