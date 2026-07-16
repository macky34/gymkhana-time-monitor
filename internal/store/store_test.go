package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"timemon/internal/domain"
)

// --- test helpers -----------------------------------------------------

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "event.sqlite3")
	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return st
}

func defaultSettings(eventName string) EventRow {
	max660 := 660
	max1600 := 1600
	return EventRow{
		EventName:        eventName,
		TimingMode:       "sensor",
		PTMode:           "add",
		PTPenaltyMS:      5000,
		HeatRanking:      false,
		RegistrationMode: "public",
		RegistrationOpen: true,
		QueueSelfEntry:   true,
		MaxCourseTimeSec: 180,
		SensorLockoutMS:  800,
		Coef: domain.Coefficients{
			TurboGasoline: 1.7,
			TurboDiesel:   1.5,
			Rotary:        1.7,
			Supercharger:  1.7,
		},
		DispClasses: []domain.DispClass{
			{Label: "~660cc", MaxCC: &max660},
			{Label: "~1600cc", MaxCC: &max1600},
			{Label: "無制限", MaxCC: nil},
		},
	}
}

// seedMinimal seeds a standard event (driver classes: 現役/学内OB/社会人,
// drivetrain classes: 2WD/4WD) so FK-constrained driver/vehicle rows can be
// created.
func seedMinimal(t *testing.T, st *Store) {
	t.Helper()
	if err := st.SeedEvent(defaultSettings("Seed Event"), []string{"現役", "学内OB", "社会人"}, []string{"2WD", "4WD"}); err != nil {
		t.Fatalf("seedMinimal: SeedEvent: %v", err)
	}
}

// activeEventID resolves the currently active event's id, failing the test
// if none is active.
func activeEventID(t *testing.T, st *Store) int64 {
	t.Helper()
	ev, ok, err := st.GetActiveEvent()
	if err != nil {
		t.Fatalf("GetActiveEvent: %v", err)
	}
	if !ok {
		t.Fatalf("GetActiveEvent: no active event")
	}
	return ev.ID
}

func queueIDs(rows []QueueRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func runIDs(rows []domain.RunRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.LogID
	}
	return out
}

func logIDs(rows []LogRow) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

// --- Open / schema ------------------------------------------------------

func TestOpenCreatesSchemaAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "event.sqlite3")

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	tables := map[string]bool{}
	rows, err := st.db.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatalf("query sqlite_master: %v", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			t.Fatalf("scan table name: %v", err)
		}
		tables[name] = true
	}
	rows.Close()
	for _, want := range []string{"events", "class_defs", "drivers", "vehicles", "entries", "queue", "logs", "sensor_events", "audit"} {
		if !tables[want] {
			t.Errorf("missing table %q after Open", want)
		}
	}

	if _, ok, err := st.GetActiveEvent(); err != nil {
		t.Fatalf("GetActiveEvent on fresh db: %v", err)
	} else if ok {
		t.Errorf("GetActiveEvent ok=true on fresh db, want false (setup mode signal)")
	}

	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-opening the same file (schema already present) must not error, and
	// data seeded before closing must survive the round trip.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open on existing schema: %v", err)
	}
	if err := st2.SeedEvent(defaultSettings("Persisted Event"), []string{"現役"}, []string{"2WD"}); err != nil {
		t.Fatalf("SeedEvent: %v", err)
	}
	if err := st2.Close(); err != nil {
		t.Fatalf("Close st2: %v", err)
	}

	st3, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open after seed: %v", err)
	}
	defer st3.Close()
	got, ok, err := st3.GetActiveEvent()
	if err != nil {
		t.Fatalf("GetActiveEvent after re-Open: %v", err)
	}
	if !ok || got.EventName != "Persisted Event" {
		t.Errorf("data did not persist across close/re-Open: ok=%v got=%+v", ok, got)
	}
}

func TestForeignKeysEnforced(t *testing.T) {
	st := newTestStore(t)
	// No class_defs seeded at all, so driver_class_id=999 cannot reference a
	// real row. This only fails if the per-connection foreign_keys=ON
	// pragma actually took effect.
	if _, err := st.CreateDriver("Ghost", 999, "tok-ghost", "user"); err == nil {
		t.Errorf("CreateDriver with dangling driver_class_id: err=nil, want FK constraint violation")
	}
}

// --- events / class_defs ----------------------------------------------

func TestSeedEventAndGetActiveEvent(t *testing.T) {
	st := newTestStore(t)

	set := defaultSettings("Test Gymkhana 2026")
	if err := st.SeedEvent(set, []string{"現役", "学内OB", "社会人"}, []string{"2WD", "4WD"}); err != nil {
		t.Fatalf("SeedEvent: %v", err)
	}

	got, ok, err := st.GetActiveEvent()
	if err != nil {
		t.Fatalf("GetActiveEvent: %v", err)
	}
	if !ok {
		t.Fatalf("GetActiveEvent ok=false after SeedEvent")
	}
	if got.Status != "active" {
		t.Errorf("Status = %q, want active", got.Status)
	}
	if got.CreatedAtMS == 0 {
		t.Errorf("CreatedAtMS = 0, want a real timestamp")
	}
	if got.ClosedAtMS != nil {
		t.Errorf("ClosedAtMS = %v, want nil for a fresh active event", *got.ClosedAtMS)
	}
	if got.EventName != set.EventName ||
		got.TimingMode != set.TimingMode ||
		got.PTMode != set.PTMode ||
		got.PTPenaltyMS != set.PTPenaltyMS ||
		got.HeatRanking != set.HeatRanking ||
		got.RegistrationMode != set.RegistrationMode ||
		got.RegistrationOpen != set.RegistrationOpen ||
		got.QueueSelfEntry != set.QueueSelfEntry ||
		got.MaxCourseTimeSec != set.MaxCourseTimeSec ||
		got.SensorLockoutMS != set.SensorLockoutMS {
		t.Errorf("GetActiveEvent roundtrip mismatch:\n got=%+v\nwant=%+v", got, set)
	}
	if got.Coef != set.Coef {
		t.Errorf("Coef roundtrip mismatch: got=%+v want=%+v", got.Coef, set.Coef)
	}
	if len(got.DispClasses) != len(set.DispClasses) {
		t.Fatalf("DispClasses length = %d, want %d", len(got.DispClasses), len(set.DispClasses))
	}
	for i := range set.DispClasses {
		wantLabel := set.DispClasses[i].Label
		gotLabel := got.DispClasses[i].Label
		if gotLabel != wantLabel {
			t.Errorf("DispClasses[%d].Label = %q, want %q", i, gotLabel, wantLabel)
		}
		wantMax := set.DispClasses[i].MaxCC
		gotMax := got.DispClasses[i].MaxCC
		if (wantMax == nil) != (gotMax == nil) {
			t.Errorf("DispClasses[%d].MaxCC nil-ness: got=%v want=%v", i, gotMax, wantMax)
		} else if wantMax != nil && *gotMax != *wantMax {
			t.Errorf("DispClasses[%d].MaxCC = %d, want %d", i, *gotMax, *wantMax)
		}
	}

	driverClasses, err := st.ListClassDefs("driver")
	if err != nil {
		t.Fatalf("ListClassDefs(driver): %v", err)
	}
	if len(driverClasses) != 3 {
		t.Fatalf("len(driverClasses) = %d, want 3", len(driverClasses))
	}
	if driverClasses[0].Label != "現役" || driverClasses[0].SortOrder != 0 {
		t.Errorf("driverClasses[0] = %+v, want Label=現役 SortOrder=0", driverClasses[0])
	}
	if driverClasses[2].Label != "社会人" || driverClasses[2].SortOrder != 2 {
		t.Errorf("driverClasses[2] = %+v, want Label=社会人 SortOrder=2", driverClasses[2])
	}

	dtClasses, err := st.ListClassDefs("drivetrain")
	if err != nil {
		t.Fatalf("ListClassDefs(drivetrain): %v", err)
	}
	if len(dtClasses) != 2 {
		t.Fatalf("len(dtClasses) = %d, want 2", len(dtClasses))
	}

	all, err := st.ListClassDefs("")
	if err != nil {
		t.Fatalf(`ListClassDefs(""): %v`, err)
	}
	if len(all) != 5 {
		t.Errorf("len(ListClassDefs(\"\")) = %d, want 5", len(all))
	}

	// UpdateEvent persists.
	got.EventName = "Renamed Event"
	got.PTPenaltyMS = 6000
	got.RegistrationOpen = false
	if err := st.UpdateEvent(got); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}
	got2, ok, err := st.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent after UpdateEvent: ok=%v err=%v", ok, err)
	}
	if got2.EventName != "Renamed Event" || got2.PTPenaltyMS != 6000 || got2.RegistrationOpen {
		t.Errorf("UpdateEvent did not persist: got=%+v", got2)
	}
}

func TestCreateEventRejectsSecondActive(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)

	if _, err := st.CreateEvent(defaultSettings("Second Event")); !errors.Is(err, ErrActiveEventExists) {
		t.Fatalf("CreateEvent while one is active: err=%v, want ErrActiveEventExists", err)
	}
}

func TestCloseEventThenCreateNewActive(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	firstID := activeEventID(t, st)

	if err := st.CloseEvent(firstID); err != nil {
		t.Fatalf("CloseEvent: %v", err)
	}
	if _, ok, err := st.GetActiveEvent(); err != nil {
		t.Fatalf("GetActiveEvent after close: %v", err)
	} else if ok {
		t.Errorf("GetActiveEvent ok=true after closing the only event")
	}

	closed, ok, err := st.GetEvent(firstID)
	if err != nil || !ok {
		t.Fatalf("GetEvent(closed): ok=%v err=%v", ok, err)
	}
	if closed.Status != "closed" || closed.ClosedAtMS == nil {
		t.Errorf("closed event = %+v, want status=closed with ClosedAtMS set", closed)
	}

	// Closing again fails: no longer 'active'.
	if err := st.CloseEvent(firstID); !errors.Is(err, ErrEventNotActive) {
		t.Errorf("CloseEvent (already closed): err=%v, want ErrEventNotActive", err)
	}

	secondID, err := st.CreateEvent(defaultSettings("Second Event"))
	if err != nil {
		t.Fatalf("CreateEvent after close: %v", err)
	}
	active, ok, err := st.GetActiveEvent()
	if err != nil || !ok || active.ID != secondID {
		t.Fatalf("GetActiveEvent after second CreateEvent: ok=%v err=%v got=%+v", ok, err, active)
	}

	events, err := st.ListEvents()
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	if len(events) != 2 || events[0].ID != secondID || events[1].ID != firstID {
		t.Fatalf("ListEvents order = %+v, want [second, first] (created_at_ms DESC)", events)
	}
}

// TestHasAdmin covers the stage-2 /setup gate: false on a freshly seeded
// event (no drivers yet), true once an admin driver is created, and false
// again does not apply once an admin has ever existed (there is no
// "un-admin" path exercised here beyond SetRole, covered below).
func TestHasAdmin(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)

	if has, err := st.HasAdmin(); err != nil {
		t.Fatalf("HasAdmin (no drivers): %v", err)
	} else if has {
		t.Errorf("HasAdmin = true before any driver exists, want false")
	}

	driverClasses, _ := st.ListClassDefs("driver")
	uid, err := st.CreateDriver("U", driverClasses[0].ID, "tok-user", "user")
	if err != nil {
		t.Fatalf("CreateDriver(user): %v", err)
	}
	if has, err := st.HasAdmin(); err != nil {
		t.Fatalf("HasAdmin (user only): %v", err)
	} else if has {
		t.Errorf("HasAdmin = true with a non-admin driver, want false")
	}

	if _, err := st.CreateDriver("A", driverClasses[0].ID, "tok-admin", "admin"); err != nil {
		t.Fatalf("CreateDriver(admin): %v", err)
	}
	if has, err := st.HasAdmin(); err != nil {
		t.Fatalf("HasAdmin (admin created): %v", err)
	} else if !has {
		t.Errorf("HasAdmin = false after creating an admin, want true")
	}

	// SetRole demoting the sole non-admin has no effect on HasAdmin either
	// way; sanity check it still reports true (the admin driver is untouched).
	if err := st.SetRole(uid, "user"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	if has, err := st.HasAdmin(); err != nil {
		t.Fatalf("HasAdmin (after unrelated SetRole): %v", err)
	} else if !has {
		t.Errorf("HasAdmin = false, want true (the admin driver is unaffected)")
	}
}

// --- drivers / vehicles / entries ---------------------------------------

func TestDriverVehicleEntryCRUD(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)

	driverClasses, err := st.ListClassDefs("driver")
	if err != nil {
		t.Fatalf("ListClassDefs(driver): %v", err)
	}
	dtClasses, err := st.ListClassDefs("drivetrain")
	if err != nil {
		t.Fatalf("ListClassDefs(drivetrain): %v", err)
	}
	driverClassID := driverClasses[0].ID
	dtClassID := dtClasses[0].ID

	// --- drivers ---
	id1, err := st.CreateDriver("山田太郎", driverClassID, "token-1", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	adminID, err := st.CreateDriver("運営花子", driverClassID, "token-admin", "admin")
	if err != nil {
		t.Fatalf("CreateDriver(admin): %v", err)
	}

	d1, ok, err := st.GetDriver(id1)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if d1.Name != "山田太郎" || d1.Role != "user" || d1.DriverClassID != driverClassID || d1.HasIcon {
		t.Errorf("GetDriver mismatch: %+v", d1)
	}
	if d1.MainVehicleID != nil {
		t.Errorf("MainVehicleID should start nil, got %v", *d1.MainVehicleID)
	}

	byToken, ok, err := st.GetDriverByToken("token-1")
	if err != nil || !ok || byToken.ID != id1 {
		t.Fatalf("GetDriverByToken: ok=%v err=%v byToken=%+v", ok, err, byToken)
	}
	if _, ok, err := st.GetDriverByToken("no-such-token"); err != nil {
		t.Fatalf("GetDriverByToken(missing): %v", err)
	} else if ok {
		t.Errorf("GetDriverByToken(missing) ok=true, want false")
	}

	allDrivers, err := st.ListDrivers()
	if err != nil {
		t.Fatalf("ListDrivers: %v", err)
	}
	if len(allDrivers) != 2 {
		t.Fatalf("ListDrivers len = %d, want 2", len(allDrivers))
	}

	if err := st.UpdateDriver(id1, "山田次郎", driverClassID); err != nil {
		t.Fatalf("UpdateDriver: %v", err)
	}
	d1Updated, _, err := st.GetDriver(id1)
	if err != nil {
		t.Fatalf("GetDriver after update: %v", err)
	}
	if d1Updated.Name != "山田次郎" {
		t.Errorf("UpdateDriver did not persist name, got %q", d1Updated.Name)
	}

	adminCount, err := st.CountAdmins()
	if err != nil {
		t.Fatalf("CountAdmins: %v", err)
	}
	if adminCount != 1 {
		t.Errorf("CountAdmins = %d, want 1", adminCount)
	}
	if err := st.SetRole(id1, "admin"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}
	adminCount, err = st.CountAdmins()
	if err != nil {
		t.Fatalf("CountAdmins after SetRole: %v", err)
	}
	if adminCount != 2 {
		t.Errorf("CountAdmins after SetRole = %d, want 2", adminCount)
	}

	if err := st.ReissueToken(id1, "token-1-new"); err != nil {
		t.Fatalf("ReissueToken: %v", err)
	}
	if _, ok, err := st.GetDriverByToken("token-1"); err != nil {
		t.Fatalf("GetDriverByToken(old): %v", err)
	} else if ok {
		t.Errorf("old token still valid after ReissueToken")
	}
	if _, ok, err := st.GetDriverByToken("token-1-new"); err != nil {
		t.Fatalf("GetDriverByToken(new): %v", err)
	} else if !ok {
		t.Errorf("new token not valid after ReissueToken")
	}

	if _, ok, err := st.GetIcon(id1); err != nil || ok {
		t.Errorf("GetIcon before SetIcon: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	if err := st.SetIcon(id1, jpeg); err != nil {
		t.Fatalf("SetIcon: %v", err)
	}
	gotIcon, ok, err := st.GetIcon(id1)
	if err != nil || !ok {
		t.Fatalf("GetIcon after SetIcon: ok=%v err=%v", ok, err)
	}
	if string(gotIcon) != string(jpeg) {
		t.Errorf("GetIcon mismatch: got=%v want=%v", gotIcon, jpeg)
	}
	d1WithIcon, _, err := st.GetDriver(id1)
	if err != nil {
		t.Fatalf("GetDriver after SetIcon: %v", err)
	}
	if !d1WithIcon.HasIcon {
		t.Errorf("HasIcon should be true after SetIcon")
	}

	// --- vehicles ---
	cc658 := 658
	newVehicle := Vehicle{Number: 8, Name: "アルトワークス", Engine: "gasoline", DisplacementCC: &cc658, ForcedInduction: true, DrivetrainClassID: dtClassID}
	vid, err := st.CreateVehicle(newVehicle)
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}

	gotVehicle, ok, err := st.GetVehicle(vid)
	if err != nil || !ok {
		t.Fatalf("GetVehicle: ok=%v err=%v", ok, err)
	}
	if gotVehicle.Number != 8 || gotVehicle.Name != "アルトワークス" || gotVehicle.Engine != "gasoline" ||
		!gotVehicle.ForcedInduction || gotVehicle.DisplacementCC == nil || *gotVehicle.DisplacementCC != 658 {
		t.Errorf("GetVehicle mismatch: %+v", gotVehicle)
	}

	evID, err := st.CreateVehicle(Vehicle{Number: 9, Name: "リーフ", Engine: "ev", DrivetrainClassID: dtClassID})
	if err != nil {
		t.Fatalf("CreateVehicle(ev): %v", err)
	}
	evVehicle, _, err := st.GetVehicle(evID)
	if err != nil {
		t.Fatalf("GetVehicle(ev): %v", err)
	}
	if evVehicle.DisplacementCC != nil {
		t.Errorf("EV DisplacementCC should be nil, got %v", *evVehicle.DisplacementCC)
	}

	if inUse, err := st.NumberInUse(8, 0); err != nil {
		t.Fatalf("NumberInUse(8,0): %v", err)
	} else if !inUse {
		t.Errorf("NumberInUse(8, 0) = false, want true")
	}
	if inUse, err := st.NumberInUse(8, vid); err != nil {
		t.Fatalf("NumberInUse(8,vid): %v", err)
	} else if inUse {
		t.Errorf("NumberInUse(8, vid) = true, want false (excludes itself)")
	}
	if inUse, err := st.NumberInUse(999, 0); err != nil {
		t.Fatalf("NumberInUse(999,0): %v", err)
	} else if inUse {
		t.Errorf("NumberInUse(999, 0) = true, want false")
	}

	gotVehicle.Name = "アルトワークスRS"
	if err := st.UpdateVehicle(gotVehicle); err != nil {
		t.Fatalf("UpdateVehicle: %v", err)
	}
	gotVehicleUpdated, _, err := st.GetVehicle(vid)
	if err != nil {
		t.Fatalf("GetVehicle after update: %v", err)
	}
	if gotVehicleUpdated.Name != "アルトワークスRS" {
		t.Errorf("UpdateVehicle did not persist, got %q", gotVehicleUpdated.Name)
	}

	vehicles, err := st.ListVehicles()
	if err != nil {
		t.Fatalf("ListVehicles: %v", err)
	}
	if len(vehicles) != 2 {
		t.Fatalf("ListVehicles len = %d, want 2", len(vehicles))
	}

	if err := st.DeleteVehicle(evID); err != nil {
		t.Fatalf("DeleteVehicle: %v", err)
	}
	vehiclesAfterDelete, err := st.ListVehicles()
	if err != nil {
		t.Fatalf("ListVehicles after delete: %v", err)
	}
	if len(vehiclesAfterDelete) != 1 {
		t.Errorf("ListVehicles after delete len = %d, want 1", len(vehiclesAfterDelete))
	}
	if _, ok, err := st.GetVehicle(evID); err != nil || !ok {
		t.Errorf("GetVehicle(soft-deleted) ok=%v err=%v, want ok=true (row must survive for history)", ok, err)
	}

	// --- entries ---
	if err := st.AddEntry(id1, vid); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	if err := st.AddEntry(id1, vid); err != nil {
		t.Errorf("AddEntry (duplicate, should be idempotent) errored: %v", err)
	}
	if err := st.AddEntry(adminID, vid); err != nil {
		t.Fatalf("AddEntry(second driver): %v", err)
	}

	byDriver, err := st.ListEntriesByDriver(id1)
	if err != nil {
		t.Fatalf("ListEntriesByDriver: %v", err)
	}
	if len(byDriver) != 1 || byDriver[0].ID != vid {
		t.Errorf("ListEntriesByDriver = %+v, want single vehicle id=%d", byDriver, vid)
	}

	byVehicle, err := st.ListDriversByVehicle(vid)
	if err != nil {
		t.Fatalf("ListDriversByVehicle: %v", err)
	}
	if len(byVehicle) != 2 {
		t.Errorf("ListDriversByVehicle len = %d, want 2", len(byVehicle))
	}

	if err := st.SetMainVehicle(id1, vid); err != nil {
		t.Fatalf("SetMainVehicle: %v", err)
	}
	d1WithMain, _, err := st.GetDriver(id1)
	if err != nil {
		t.Fatalf("GetDriver after SetMainVehicle: %v", err)
	}
	if d1WithMain.MainVehicleID == nil || *d1WithMain.MainVehicleID != vid {
		t.Errorf("MainVehicleID after SetMainVehicle = %v, want %d", d1WithMain.MainVehicleID, vid)
	}

	if err := st.DeleteEntry(adminID, vid); err != nil {
		t.Fatalf("DeleteEntry: %v", err)
	}
	byVehicleAfter, err := st.ListDriversByVehicle(vid)
	if err != nil {
		t.Fatalf("ListDriversByVehicle after DeleteEntry: %v", err)
	}
	if len(byVehicleAfter) != 1 {
		t.Errorf("ListDriversByVehicle after DeleteEntry len = %d, want 1", len(byVehicleAfter))
	}
}

// --- vehicle icon / auto main-vehicle -------------------------------------

func TestVehicleIconRoundtrip(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	dtClasses, _ := st.ListClassDefs("drivetrain")

	vid, err := st.CreateVehicle(Vehicle{Number: 1, Name: "V", Engine: "gasoline", DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}

	if _, ok, err := st.GetVehicleIcon(vid); err != nil || ok {
		t.Errorf("GetVehicleIcon before SetVehicleIcon: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
	v, ok, err := st.GetVehicle(vid)
	if err != nil || !ok || v.HasIcon {
		t.Fatalf("GetVehicle before SetVehicleIcon: ok=%v err=%v HasIcon=%v, want HasIcon=false", ok, err, v.HasIcon)
	}

	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xD9}
	if err := st.SetVehicleIcon(vid, jpeg); err != nil {
		t.Fatalf("SetVehicleIcon: %v", err)
	}
	gotIcon, ok, err := st.GetVehicleIcon(vid)
	if err != nil || !ok {
		t.Fatalf("GetVehicleIcon after SetVehicleIcon: ok=%v err=%v", ok, err)
	}
	if string(gotIcon) != string(jpeg) {
		t.Errorf("GetVehicleIcon mismatch: got=%v want=%v", gotIcon, jpeg)
	}
	vWithIcon, _, err := st.GetVehicle(vid)
	if err != nil {
		t.Fatalf("GetVehicle after SetVehicleIcon: %v", err)
	}
	if !vWithIcon.HasIcon {
		t.Errorf("HasIcon should be true after SetVehicleIcon")
	}
}

func TestAddEntryAutoSetsMainVehicleOnce(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")

	driverID, err := st.CreateDriver("山田太郎", driverClasses[0].ID, "tok-auto-main", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	v1, err := st.CreateVehicle(Vehicle{Number: 1, Name: "V1", Engine: "gasoline", DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle(1): %v", err)
	}
	v2, err := st.CreateVehicle(Vehicle{Number: 2, Name: "V2", Engine: "gasoline", DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle(2): %v", err)
	}

	// First linked vehicle becomes the main vehicle automatically.
	if err := st.AddEntry(driverID, v1); err != nil {
		t.Fatalf("AddEntry(v1): %v", err)
	}
	d, _, err := st.GetDriver(driverID)
	if err != nil {
		t.Fatalf("GetDriver after AddEntry(v1): %v", err)
	}
	if d.MainVehicleID == nil || *d.MainVehicleID != v1 {
		t.Fatalf("MainVehicleID after AddEntry(v1) = %v, want %d", d.MainVehicleID, v1)
	}

	// A second linked vehicle must not overwrite the already-set main vehicle.
	if err := st.AddEntry(driverID, v2); err != nil {
		t.Fatalf("AddEntry(v2): %v", err)
	}
	d, _, err = st.GetDriver(driverID)
	if err != nil {
		t.Fatalf("GetDriver after AddEntry(v2): %v", err)
	}
	if d.MainVehicleID == nil || *d.MainVehicleID != v1 {
		t.Errorf("MainVehicleID after AddEntry(v2) = %v, want unchanged %d", d.MainVehicleID, v1)
	}
}

// --- queue ---------------------------------------------------------------

func TestQueueEnqueueReorderAndStatus(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventID := activeEventID(t, st)
	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")

	mkDriver := func(name, token string) int64 {
		id, err := st.CreateDriver(name, driverClasses[0].ID, token, "user")
		if err != nil {
			t.Fatalf("CreateDriver(%s): %v", name, err)
		}
		return id
	}
	mkVehicle := func(number int) int64 {
		cc := 1500
		id, err := st.CreateVehicle(Vehicle{Number: number, Name: "V", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
		if err != nil {
			t.Fatalf("CreateVehicle(%d): %v", number, err)
		}
		return id
	}

	d1, d2, d3 := mkDriver("A", "ta"), mkDriver("B", "tb"), mkDriver("C", "tc")
	v1, v2, v3 := mkVehicle(1), mkVehicle(2), mkVehicle(3)

	qid1, err := st.Enqueue(eventID, d1, v1, nil)
	if err != nil {
		t.Fatalf("Enqueue 1: %v", err)
	}
	selfCreator := d2
	qid2, err := st.Enqueue(eventID, d2, v2, &selfCreator)
	if err != nil {
		t.Fatalf("Enqueue 2: %v", err)
	}
	qid3, err := st.Enqueue(eventID, d3, v3, nil)
	if err != nil {
		t.Fatalf("Enqueue 3: %v", err)
	}

	if _, err := st.Enqueue(eventID, d1, v1, nil); !errors.Is(err, ErrAlreadyWaiting) {
		t.Errorf("Enqueue duplicate: err=%v, want ErrAlreadyWaiting", err)
	}

	waiting, err := st.ListQueue(eventID, "waiting")
	if err != nil {
		t.Fatalf("ListQueue(waiting): %v", err)
	}
	if len(waiting) != 3 {
		t.Fatalf("ListQueue(waiting) len = %d, want 3", len(waiting))
	}
	for i, want := range []int64{qid1, qid2, qid3} {
		if waiting[i].ID != want {
			t.Errorf("waiting[%d].ID = %d, want %d (position order = %v)", i, waiting[i].ID, want, queueIDs(waiting))
		}
		if waiting[i].EventID != eventID {
			t.Errorf("waiting[%d].EventID = %d, want %d", i, waiting[i].EventID, eventID)
		}
	}
	if waiting[0].Position != 1.0 || waiting[1].Position != 2.0 || waiting[2].Position != 3.0 {
		t.Errorf("positions = %v, %v, %v; want 1,2,3", waiting[0].Position, waiting[1].Position, waiting[2].Position)
	}
	if waiting[1].CreatedBy == nil || *waiting[1].CreatedBy != selfCreator {
		t.Errorf("CreatedBy not persisted for qid2: %+v", waiting[1])
	}

	if err := st.Reorder(qid3, 1.5); err != nil {
		t.Fatalf("Reorder: %v", err)
	}
	waiting, err = st.ListQueue(eventID, "waiting")
	if err != nil {
		t.Fatalf("ListQueue(waiting) after Reorder: %v", err)
	}
	if waiting[0].ID != qid1 || waiting[1].ID != qid3 || waiting[2].ID != qid2 {
		t.Errorf("after Reorder order = %v, want [qid1, qid3, qid2]", queueIDs(waiting))
	}

	row1, ok, err := st.GetQueueRow(qid1)
	if err != nil || !ok {
		t.Fatalf("GetQueueRow: ok=%v err=%v", ok, err)
	}
	if row1.Status != "waiting" || row1.DriverID != d1 || row1.VehicleID != v1 {
		t.Errorf("GetQueueRow mismatch: %+v", row1)
	}

	if err := st.SetQueueStatus(qid1, "on_course"); err != nil {
		t.Fatalf("SetQueueStatus: %v", err)
	}
	tStart := int64(1_720_000_000_000_000)
	if err := st.SetStart(qid1, &tStart); err != nil {
		t.Fatalf("SetStart: %v", err)
	}
	row1, _, err = st.GetQueueRow(qid1)
	if err != nil {
		t.Fatalf("GetQueueRow after SetStart: %v", err)
	}
	if row1.Status != "on_course" || row1.TStartUS == nil || *row1.TStartUS != tStart {
		t.Errorf("after SetQueueStatus/SetStart: %+v", row1)
	}

	if err := st.SetQueueStatus(qid3, "on_course"); err != nil {
		t.Fatalf("SetQueueStatus(qid3): %v", err)
	}
	onCourse, err := st.ListQueue(eventID, "on_course")
	if err != nil {
		t.Fatalf("ListQueue(on_course): %v", err)
	}
	if len(onCourse) != 2 || onCourse[0].ID != qid1 || onCourse[1].ID != qid3 {
		t.Errorf("ListQueue(on_course) = %v, want id order [qid1, qid3]", queueIDs(onCourse))
	}
	waitingAfter, err := st.ListQueue(eventID, "waiting")
	if err != nil {
		t.Fatalf("ListQueue(waiting) after two dequeues: %v", err)
	}
	if len(waitingAfter) != 1 || waitingAfter[0].ID != qid2 {
		t.Errorf("ListQueue(waiting) after two dequeues = %v, want [qid2]", queueIDs(waitingAfter))
	}

	// Undo-start: clear timestamp, flip back to waiting.
	if err := st.SetStart(qid1, nil); err != nil {
		t.Fatalf("SetStart(nil): %v", err)
	}
	if err := st.SetQueueStatus(qid1, "waiting"); err != nil {
		t.Fatalf("SetQueueStatus(back to waiting): %v", err)
	}
	row1, _, err = st.GetQueueRow(qid1)
	if err != nil {
		t.Fatalf("GetQueueRow after undo-start: %v", err)
	}
	if row1.TStartUS != nil {
		t.Errorf("TStartUS not cleared: %v", *row1.TStartUS)
	}

	if err := st.SetMC(qid3, true); err != nil {
		t.Fatalf("SetMC: %v", err)
	}
	row3, _, err := st.GetQueueRow(qid3)
	if err != nil {
		t.Fatalf("GetQueueRow(qid3): %v", err)
	}
	if !row3.MCFlag {
		t.Errorf("MCFlag not set")
	}
	if err := st.SetMC(qid3, false); err != nil {
		t.Fatalf("SetMC(false): %v", err)
	}
	row3, _, err = st.GetQueueRow(qid3)
	if err != nil {
		t.Fatalf("GetQueueRow(qid3) after clear: %v", err)
	}
	if row3.MCFlag {
		t.Errorf("MCFlag not cleared")
	}
}

// TestCancelWaiting covers the event-close helper: every 'waiting' row of
// the target event moves to 'canceled', on_course rows are left alone, and
// another event's waiting rows are untouched.
func TestCancelWaiting(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventA := activeEventID(t, st)
	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")

	cc := 1200
	d1, err := st.CreateDriver("D1", driverClasses[0].ID, "tok-cw1", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	d2, err := st.CreateDriver("D2", driverClasses[0].ID, "tok-cw2", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	v1, err := st.CreateVehicle(Vehicle{Number: 1, Name: "V1", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}
	v2, err := st.CreateVehicle(Vehicle{Number: 2, Name: "V2", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}

	qWaiting, err := st.Enqueue(eventA, d1, v1, nil)
	if err != nil {
		t.Fatalf("Enqueue(waiting): %v", err)
	}
	qOnCourse, err := st.Enqueue(eventA, d2, v2, nil)
	if err != nil {
		t.Fatalf("Enqueue(on_course-to-be): %v", err)
	}
	if err := st.SetQueueStatus(qOnCourse, "on_course"); err != nil {
		t.Fatalf("SetQueueStatus: %v", err)
	}

	if err := st.CloseEvent(eventA); err != nil {
		t.Fatalf("CloseEvent: %v", err)
	}
	eventB, err := st.CreateEvent(defaultSettings("Event B"))
	if err != nil {
		t.Fatalf("CreateEvent(eventB): %v", err)
	}
	qOtherEvent, err := st.Enqueue(eventB, d1, v1, nil)
	if err != nil {
		t.Fatalf("Enqueue(eventB): %v", err)
	}

	if err := st.CancelWaiting(eventA); err != nil {
		t.Fatalf("CancelWaiting: %v", err)
	}

	waitingRow, _, err := st.GetQueueRow(qWaiting)
	if err != nil {
		t.Fatalf("GetQueueRow(qWaiting): %v", err)
	}
	if waitingRow.Status != "canceled" {
		t.Errorf("waiting row status = %q, want canceled", waitingRow.Status)
	}
	onCourseRow, _, err := st.GetQueueRow(qOnCourse)
	if err != nil {
		t.Fatalf("GetQueueRow(qOnCourse): %v", err)
	}
	if onCourseRow.Status != "on_course" {
		t.Errorf("on_course row status = %q, want unchanged on_course", onCourseRow.Status)
	}
	otherEventRow, _, err := st.GetQueueRow(qOtherEvent)
	if err != nil {
		t.Fatalf("GetQueueRow(qOtherEvent): %v", err)
	}
	if otherEventRow.Status != "waiting" {
		t.Errorf("other event's waiting row status = %q, want unchanged waiting", otherEventRow.Status)
	}
}

func TestReorderRenumbersWhenPositionsConverge(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventID := activeEventID(t, st)
	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")

	var qids []int64
	for i := 0; i < 3; i++ {
		did, err := st.CreateDriver(fmt.Sprintf("D%d", i), driverClasses[0].ID, fmt.Sprintf("tok%d", i), "user")
		if err != nil {
			t.Fatalf("CreateDriver: %v", err)
		}
		cc := 1000
		vid, err := st.CreateVehicle(Vehicle{Number: i + 1, Name: "V", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
		if err != nil {
			t.Fatalf("CreateVehicle: %v", err)
		}
		qid, err := st.Enqueue(eventID, did, vid, nil)
		if err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
		qids = append(qids, qid)
	}
	// qids now sit at positions 1.0, 2.0, 3.0.

	// Move qids[1] to within 1e-10 of qids[0]'s position 1.0 -- below the
	// 1e-9 threshold, which must trigger a full 1.0-increment renumber.
	if err := st.Reorder(qids[1], 1.0+1e-10); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	waiting, err := st.ListQueue(eventID, "waiting")
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	if len(waiting) != 3 {
		t.Fatalf("waiting len = %d, want 3", len(waiting))
	}
	wantOrder := []int64{qids[0], qids[1], qids[2]}
	for i, want := range wantOrder {
		if waiting[i].ID != want {
			t.Fatalf("waiting[%d].ID = %d, want %d (order=%v)", i, waiting[i].ID, want, queueIDs(waiting))
		}
		wantPos := float64(i + 1)
		if waiting[i].Position != wantPos {
			t.Errorf("waiting[%d].Position = %v, want %v (renumber expected)", i, waiting[i].Position, wantPos)
		}
	}
}

func TestSetPTGuard(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventID := activeEventID(t, st)
	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")
	did, err := st.CreateDriver("D", driverClasses[0].ID, "tok", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	cc := 1000
	vid, err := st.CreateVehicle(Vehicle{Number: 1, Name: "V", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}
	qid, err := st.Enqueue(eventID, did, vid, nil)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	newVal, err := st.SetPT(qid, -1)
	if !errors.Is(err, ErrPTBelowZero) {
		t.Fatalf("SetPT(-1) from 0: err=%v, want ErrPTBelowZero", err)
	}
	if newVal != 0 {
		t.Errorf("SetPT(-1) from 0: newVal=%d, want unchanged 0", newVal)
	}
	row, _, err := st.GetQueueRow(qid)
	if err != nil {
		t.Fatalf("GetQueueRow: %v", err)
	}
	if row.PTCount != 0 {
		t.Errorf("pt_count changed despite guard: %d", row.PTCount)
	}

	newVal, err = st.SetPT(qid, 1)
	if err != nil {
		t.Fatalf("SetPT(+1): %v", err)
	}
	if newVal != 1 {
		t.Errorf("SetPT(+1) = %d, want 1", newVal)
	}

	newVal, err = st.SetPT(qid, 1)
	if err != nil {
		t.Fatalf("SetPT(+1) again: %v", err)
	}
	if newVal != 2 {
		t.Errorf("SetPT(+1) again = %d, want 2", newVal)
	}

	newVal, err = st.SetPT(qid, -2)
	if err != nil {
		t.Fatalf("SetPT(-2): %v", err)
	}
	if newVal != 0 {
		t.Errorf("SetPT(-2) = %d, want 0", newVal)
	}

	// Exactly hitting zero is fine; going one further below must guard again.
	newVal, err = st.SetPT(qid, -1)
	if !errors.Is(err, ErrPTBelowZero) {
		t.Errorf("SetPT(-1) from 0 (second time): err=%v, want ErrPTBelowZero", err)
	}
	if newVal != 0 {
		t.Errorf("newVal = %d, want 0", newVal)
	}
}

// --- logs ------------------------------------------------------------------

func TestLogsCRUDAndListRuns(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventID := activeEventID(t, st)
	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")

	d1, err := st.CreateDriver("D1", driverClasses[0].ID, "t1", "user")
	if err != nil {
		t.Fatalf("CreateDriver d1: %v", err)
	}
	d2, err := st.CreateDriver("D2", driverClasses[0].ID, "t2", "user")
	if err != nil {
		t.Fatalf("CreateDriver d2: %v", err)
	}
	cc := 1500
	v1, err := st.CreateVehicle(Vehicle{Number: 1, Name: "V1", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle v1: %v", err)
	}
	v2, err := st.CreateVehicle(Vehicle{Number: 2, Name: "V2", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle v2: %v", err)
	}

	p := func(v int64) *int64 { return &v }
	mkLog := func(driverID, vehicleID *int64, rawMS int, ts int64, source string) int64 {
		id, err := st.InsertLog(LogRow{EventID: eventID, DriverID: driverID, VehicleID: vehicleID, RawMS: rawMS, TimestampMS: ts, Source: source})
		if err != nil {
			t.Fatalf("InsertLog: %v", err)
		}
		return id
	}

	l1 := mkLog(p(d1), p(v1), 84310, 1000, "sensor")
	l2 := mkLog(p(d1), p(v1), 83000, 2000, "sensor")
	l3 := mkLog(p(d2), p(v2), 90000, 1500, "manual")
	lUnassigned := mkLog(nil, p(v2), 88000, 3000, "sensor")

	gotLog, ok, err := st.GetLog(l1)
	if err != nil || !ok {
		t.Fatalf("GetLog: ok=%v err=%v", ok, err)
	}
	if gotLog.RawMS != 84310 || gotLog.Source != "sensor" || gotLog.DriverID == nil || *gotLog.DriverID != d1 {
		t.Errorf("GetLog mismatch: %+v", gotLog)
	}
	if gotLog.EventID != eventID {
		t.Errorf("GetLog.EventID = %d, want %d", gotLog.EventID, eventID)
	}

	editedAt := int64(9999)
	gotLog.RawMS = 84000
	gotLog.EditedAt = &editedAt
	if err := st.UpdateLog(gotLog); err != nil {
		t.Fatalf("UpdateLog: %v", err)
	}
	gotLog2, _, err := st.GetLog(l1)
	if err != nil {
		t.Fatalf("GetLog after update: %v", err)
	}
	if gotLog2.RawMS != 84000 || gotLog2.EditedAt == nil || *gotLog2.EditedAt != 9999 {
		t.Errorf("UpdateLog did not persist: %+v", gotLog2)
	}

	runs, err := st.ListRuns(eventID)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("ListRuns len = %d, want 3 (unassigned excluded); got ids=%v", len(runs), runIDs(runs))
	}
	if runs[0].LogID != l1 || runs[1].LogID != l3 || runs[2].LogID != l2 {
		t.Errorf("ListRuns order = %v, want [%d,%d,%d] (timestamp_ms ascending)", runIDs(runs), l1, l3, l2)
	}
	if runs[0].Combo != (domain.ComboKey{DriverID: d1, VehicleID: v1}) {
		t.Errorf("ListRuns[0].Combo = %+v", runs[0].Combo)
	}

	byCombo, err := st.ListRunsByCombo(eventID, d1, v1)
	if err != nil {
		t.Fatalf("ListRunsByCombo: %v", err)
	}
	if len(byCombo) != 2 {
		t.Fatalf("ListRunsByCombo len = %d, want 2", len(byCombo))
	}

	unassigned, err := st.ListUnassignedLogs(eventID)
	if err != nil {
		t.Fatalf("ListUnassignedLogs: %v", err)
	}
	if len(unassigned) != 1 || unassigned[0].ID != lUnassigned {
		t.Fatalf("ListUnassignedLogs = %v, want [%d]", logIDs(unassigned), lUnassigned)
	}

	if err := st.SoftDeleteLog(l2); err != nil {
		t.Fatalf("SoftDeleteLog: %v", err)
	}
	runsAfterDelete, err := st.ListRuns(eventID)
	if err != nil {
		t.Fatalf("ListRuns after SoftDeleteLog: %v", err)
	}
	if len(runsAfterDelete) != 2 {
		t.Errorf("ListRuns after SoftDeleteLog len = %d, want 2", len(runsAfterDelete))
	}
	gotDeleted, ok, err := st.GetLog(l2)
	if err != nil || !ok {
		t.Fatalf("GetLog(soft-deleted): ok=%v err=%v", ok, err)
	}
	if !gotDeleted.IsDeleted {
		t.Errorf("IsDeleted = false after SoftDeleteLog")
	}

	page, total, err := st.ListLogs(eventID, 2, 0)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if total != 4 {
		t.Errorf("ListLogs total = %d, want 4", total)
	}
	if len(page) != 2 {
		t.Fatalf("ListLogs page len = %d, want 2", len(page))
	}
	if page[0].ID != lUnassigned {
		t.Errorf("ListLogs[0].ID = %d, want %d (timestamp_ms DESC, newest first)", page[0].ID, lUnassigned)
	}
	page2, _, err := st.ListLogs(eventID, 2, 2)
	if err != nil {
		t.Fatalf("ListLogs page2: %v", err)
	}
	if len(page2) != 2 {
		t.Fatalf("ListLogs page2 len = %d, want 2", len(page2))
	}

	if err := st.HardDeleteLog(l3); err != nil {
		t.Fatalf("HardDeleteLog: %v", err)
	}
	if _, ok, err := st.GetLog(l3); err != nil {
		t.Fatalf("GetLog after HardDeleteLog: %v", err)
	} else if ok {
		t.Errorf("GetLog after HardDeleteLog: ok=true, want false")
	}
	_, total, err = st.ListLogs(eventID, 10, 0)
	if err != nil {
		t.Fatalf("ListLogs after HardDeleteLog: %v", err)
	}
	if total != 3 {
		t.Errorf("ListLogs total after HardDeleteLog = %d, want 3", total)
	}
}

// TestQueueAndLogsScopedPerEvent verifies that two events on the same store
// keep independent queue/logs: entries created for event A must never leak
// into event B's ListQueue/ListRuns results (Architecture wiki multi-event
// isolation requirement).
func TestQueueAndLogsScopedPerEvent(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventA := activeEventID(t, st)
	if err := st.CloseEvent(eventA); err != nil {
		t.Fatalf("CloseEvent(eventA): %v", err)
	}
	eventB, err := st.CreateEvent(defaultSettings("Event B"))
	if err != nil {
		t.Fatalf("CreateEvent(eventB): %v", err)
	}

	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")
	did, err := st.CreateDriver("D", driverClasses[0].ID, "tok-scope", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	cc := 1200
	vid, err := st.CreateVehicle(Vehicle{Number: 1, Name: "V", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}

	// One queue entry + one log in event A, none in event B.
	if _, err := st.Enqueue(eventA, did, vid, nil); err != nil {
		t.Fatalf("Enqueue(eventA): %v", err)
	}
	if _, err := st.InsertLog(LogRow{EventID: eventA, DriverID: &did, VehicleID: &vid, RawMS: 80000, TimestampMS: 1000, Source: "sensor"}); err != nil {
		t.Fatalf("InsertLog(eventA): %v", err)
	}

	waitingA, err := st.ListQueue(eventA, "waiting")
	if err != nil {
		t.Fatalf("ListQueue(eventA): %v", err)
	}
	if len(waitingA) != 1 {
		t.Fatalf("ListQueue(eventA, waiting) len = %d, want 1", len(waitingA))
	}
	waitingB, err := st.ListQueue(eventB, "waiting")
	if err != nil {
		t.Fatalf("ListQueue(eventB): %v", err)
	}
	if len(waitingB) != 0 {
		t.Fatalf("ListQueue(eventB, waiting) len = %d, want 0 (must not see eventA's queue)", len(waitingB))
	}

	runsA, err := st.ListRuns(eventA)
	if err != nil {
		t.Fatalf("ListRuns(eventA): %v", err)
	}
	if len(runsA) != 1 {
		t.Fatalf("ListRuns(eventA) len = %d, want 1", len(runsA))
	}
	runsB, err := st.ListRuns(eventB)
	if err != nil {
		t.Fatalf("ListRuns(eventB): %v", err)
	}
	if len(runsB) != 0 {
		t.Fatalf("ListRuns(eventB) len = %d, want 0 (must not see eventA's logs)", len(runsB))
	}

	_, totalA, err := st.ListLogs(eventA, 10, 0)
	if err != nil {
		t.Fatalf("ListLogs(eventA): %v", err)
	}
	if totalA != 1 {
		t.Errorf("ListLogs(eventA) total = %d, want 1", totalA)
	}
	_, totalB, err := st.ListLogs(eventB, 10, 0)
	if err != nil {
		t.Fatalf("ListLogs(eventB): %v", err)
	}
	if totalB != 0 {
		t.Errorf("ListLogs(eventB) total = %d, want 0 (must not see eventA's logs)", totalB)
	}
}

// --- sensor / audit / snapshot ---------------------------------------------

func TestInsertSensorEventDedup(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventID := activeEventID(t, st)

	ok, err := st.InsertSensorEvent("start", 42, 1, 1_000_000, 1_000_100, nil)
	if err != nil {
		t.Fatalf("InsertSensorEvent (first): %v", err)
	}
	if !ok {
		t.Errorf("InsertSensorEvent (first) ok=false, want true")
	}

	// Same (sensor_id, boot_id, seq) resent twice more (the 3x UDP resend) ->
	// deduplicated both times.
	if ok, err := st.InsertSensorEvent("start", 42, 1, 1_000_000, 1_000_150, nil); err != nil {
		t.Fatalf("InsertSensorEvent (dup 1): %v", err)
	} else if ok {
		t.Errorf("InsertSensorEvent (dup 1) ok=true, want false")
	}
	if ok, err := st.InsertSensorEvent("start", 42, 1, 1_000_000, 1_000_200, nil); err != nil {
		t.Fatalf("InsertSensorEvent (dup 2): %v", err)
	} else if ok {
		t.Errorf("InsertSensorEvent (dup 2) ok=true, want false")
	}

	if ok, err := st.InsertSensorEvent("start", 42, 2, 1_000_500, 1_000_600, nil); err != nil {
		t.Fatalf("InsertSensorEvent (seq 2): %v", err)
	} else if !ok {
		t.Errorf("InsertSensorEvent (seq 2) ok=false, want true")
	}

	if ok, err := st.InsertSensorEvent("goal", 42, 1, 1_000_700, 1_000_800, &eventID); err != nil {
		t.Fatalf("InsertSensorEvent (goal sensor, same boot/seq): %v", err)
	} else if !ok {
		t.Errorf("InsertSensorEvent (goal) ok=false, want true (sensor_id differs)")
	}

	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM sensor_events`).Scan(&count); err != nil {
		t.Fatalf("count sensor_events: %v", err)
	}
	if count != 3 {
		t.Errorf("sensor_events row count = %d, want 3", count)
	}

	var gotEventID sql.NullInt64
	if err := st.db.QueryRow(`SELECT event_id FROM sensor_events WHERE sensor_id = 'goal'`).Scan(&gotEventID); err != nil {
		t.Fatalf("query goal event_id: %v", err)
	}
	if !gotEventID.Valid || gotEventID.Int64 != eventID {
		t.Errorf("goal row event_id = %+v, want %d", gotEventID, eventID)
	}
}

func TestAppendAudit(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventID := activeEventID(t, st)
	driverID := int64(7)
	if err := st.AppendAudit(123456, &driverID, "log.edit", `{"log_id":1}`, nil); err != nil {
		t.Fatalf("AppendAudit: %v", err)
	}
	if err := st.AppendAudit(123457, nil, "queue.launch", `{}`, &eventID); err != nil {
		t.Fatalf("AppendAudit(nil driver): %v", err)
	}

	var count int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM audit`).Scan(&count); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if count != 2 {
		t.Errorf("audit row count = %d, want 2", count)
	}

	var action string
	var driverIDCol sql.NullInt64
	if err := st.db.QueryRow(`SELECT action, driver_id FROM audit WHERE at_ms = 123456`).Scan(&action, &driverIDCol); err != nil {
		t.Fatalf("query audit row: %v", err)
	}
	if action != "log.edit" || !driverIDCol.Valid || driverIDCol.Int64 != 7 {
		t.Errorf("audit row mismatch: action=%q driver_id=%+v", action, driverIDCol)
	}

	var gotEventID sql.NullInt64
	if err := st.db.QueryRow(`SELECT event_id FROM audit WHERE at_ms = 123457`).Scan(&gotEventID); err != nil {
		t.Fatalf("query audit event_id: %v", err)
	}
	if !gotEventID.Valid || gotEventID.Int64 != eventID {
		t.Errorf("audit event_id = %+v, want %d", gotEventID, eventID)
	}
}

func TestVacuumInto(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)

	dir := t.TempDir()
	dest := filepath.Join(dir, "snapshots", "out.sqlite3")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	if err := st.VacuumInto(dest); err != nil {
		t.Fatalf("VacuumInto: %v", err)
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat vacuum output: %v", err)
	}
	if fi.Size() == 0 {
		t.Errorf("vacuum output file is empty")
	}

	copySt, err := Open(dest)
	if err != nil {
		t.Fatalf("Open(vacuum copy): %v", err)
	}
	defer copySt.Close()
	got, ok, err := copySt.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent on vacuum copy: ok=%v err=%v", ok, err)
	}
	if got.EventName != "Seed Event" {
		t.Errorf("vacuum copy EventName = %q, want %q", got.EventName, "Seed Event")
	}

	// VACUUM INTO an existing destination must fail rather than overwrite.
	if err := st.VacuumInto(dest); err == nil {
		t.Errorf("VacuumInto into existing file: err=nil, want error")
	}
}

// --- concurrency -----------------------------------------------------------

func TestConcurrentEnqueueIsSerialized(t *testing.T) {
	st := newTestStore(t)
	seedMinimal(t, st)
	eventID := activeEventID(t, st)
	driverClasses, _ := st.ListClassDefs("driver")
	dtClasses, _ := st.ListClassDefs("drivetrain")

	const n = 20
	driverIDs := make([]int64, n)
	vehicleIDs := make([]int64, n)
	for i := 0; i < n; i++ {
		did, err := st.CreateDriver(fmt.Sprintf("D%d", i), driverClasses[0].ID, fmt.Sprintf("tok%d", i), "user")
		if err != nil {
			t.Fatalf("CreateDriver: %v", err)
		}
		cc := 1000
		vid, err := st.CreateVehicle(Vehicle{Number: i + 1, Name: "V", Engine: "gasoline", DisplacementCC: &cc, DrivetrainClassID: dtClasses[0].ID})
		if err != nil {
			t.Fatalf("CreateVehicle: %v", err)
		}
		driverIDs[i] = did
		vehicleIDs[i] = vid
	}

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.Enqueue(eventID, driverIDs[i], vehicleIDs[i], nil)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("Enqueue[%d]: %v", i, err)
		}
	}

	waiting, err := st.ListQueue(eventID, "waiting")
	if err != nil {
		t.Fatalf("ListQueue: %v", err)
	}
	if len(waiting) != n {
		t.Fatalf("waiting len = %d, want %d", len(waiting), n)
	}
	seen := map[float64]bool{}
	for _, q := range waiting {
		if seen[q.Position] {
			t.Errorf("duplicate position %v found -- concurrent Enqueue not serialized correctly", q.Position)
		}
		seen[q.Position] = true
	}
	if len(seen) != n {
		t.Errorf("distinct positions = %d, want %d", len(seen), n)
	}
}
