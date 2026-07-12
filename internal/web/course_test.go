package web

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"timemon/internal/domain"
	"timemon/internal/snapshot"
	"timemon/internal/sse"
	"timemon/internal/store"
)

// newTestServer builds a fully wired Server backed by a fresh temp-file DB
// with one event seeded, plus one admin driver and one vehicle entered into
// the waiting queue. It returns the server, the seeded queue row id, and the
// driver/vehicle ids. The finish grace window is shortened so grace-window
// tests do not sleep for the production 3s.
func newTestServer(t *testing.T, timingMode string) (srv *Server, queueID, driverID, vehicleID int64) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.sqlite3")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	set := store.SettingsRow{
		EventName:        "test",
		TimingMode:       timingMode,
		PTMode:           "add",
		PTPenaltyMS:      5000,
		RegistrationMode: "public",
		RegistrationOpen: true,
		QueueSelfEntry:   true,
		MaxCourseTimeSec: 180,
		SensorLockoutMS:  800,
		Coef:             domain.Coefficients{TurboGasoline: 1.7, TurboDiesel: 1.5, Rotary: 1.7, Supercharger: 1.7},
		DispClasses:      []domain.DispClass{{Label: "~1600cc", MaxCC: intp(1600)}, {Label: "無制限", MaxCC: nil}},
	}
	if err := st.SeedEvent(set, []string{"現役"}, []string{"2WD"}); err != nil {
		t.Fatalf("SeedEvent: %v", err)
	}

	driverClass, err := st.ListClassDefs("driver")
	if err != nil || len(driverClass) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	dtClass, err := st.ListClassDefs("drivetrain")
	if err != nil || len(dtClass) == 0 {
		t.Fatalf("ListClassDefs drivetrain: %v", err)
	}

	driverID, err = st.CreateDriver("牧野", driverClass[0].ID, "tok-admin", "admin")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	vehicleID, err = st.CreateVehicle(store.Vehicle{
		Number: 3, Name: "アルトワークス", Engine: "gasoline",
		DisplacementCC: intp(658), ForcedInduction: true, DrivetrainClassID: dtClass[0].ID,
	})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}
	if err := st.AddEntry(driverID, vehicleID); err != nil {
		t.Fatalf("AddEntry: %v", err)
	}
	queueID, err = st.Enqueue(driverID, vehicleID, nil)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	hub := sse.NewHub()
	snap := snapshot.New(st)
	srv, err = NewServer(st, hub, snap, "http://test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	srv.course.graceMS = 40 // keep grace-window tests fast
	return srv, queueID, driverID, vehicleID
}

func intp(v int) *int { return &v }

// launchManual moves the seeded waiting row onto the course with a start
// time already set (RUNNING), as a manual-mode launch would.
func launchManual(t *testing.T, srv *Server, queueID int64, tStartUS int64) store.QueueRow {
	t.Helper()
	if err := srv.Store.SetQueueStatus(queueID, "on_course"); err != nil {
		t.Fatalf("SetQueueStatus: %v", err)
	}
	if err := srv.Store.SetStart(queueID, &tStartUS); err != nil {
		t.Fatalf("SetStart: %v", err)
	}
	row, ok, err := srv.Store.GetQueueRow(queueID)
	if err != nil || !ok {
		t.Fatalf("GetQueueRow: %v ok=%v", err, ok)
	}
	return row
}

func rankingRowCount(t *testing.T, srv *Server) int {
	t.Helper()
	data, err := srv.Snap.Ranking()
	if err != nil {
		t.Fatalf("Ranking: %v", err)
	}
	var resp struct {
		Rows []json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal ranking: %v", err)
	}
	return len(resp.Rows)
}

// TestFinishGraceConfirm covers the happy finish path: finishCar writes the
// log immediately and opens the grace window; once it elapses the queue row
// becomes done and the result shows up in the ranking.
func TestFinishGraceConfirm(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "manual")
	t0 := time.Now().UnixMilli() * 1000
	row := launchManual(t, srv, queueID, t0)

	tGoal := t0 + 12_345*1000 // 12.345s later, in microseconds
	if err := srv.course.finishCar(row, tGoal, "manual"); err != nil {
		t.Fatalf("finishCar: %v", err)
	}

	// During the grace window the finish is pending and the log already exists.
	if !srv.course.isPending(queueID) {
		t.Fatal("expected finish to be pending during grace window")
	}
	logs, _, err := srv.Store.ListLogs(10, 0)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(logs) != 1 || logs[0].RawMS != 12345 {
		t.Fatalf("expected 1 log raw_ms=12345, got %+v", logs)
	}

	// After the grace window elapses the row is confirmed done and ranked.
	time.Sleep(120 * time.Millisecond)
	if srv.course.isPending(queueID) {
		t.Fatal("finish still pending after grace window")
	}
	qrow, _, _ := srv.Store.GetQueueRow(queueID)
	if qrow.Status != "done" {
		t.Fatalf("queue status = %q, want done", qrow.Status)
	}
	if n := rankingRowCount(t, srv); n != 1 {
		t.Fatalf("ranking rows = %d, want 1", n)
	}
}

// TestUndoGoalWithinGrace covers undo-goal: the tentative log is removed and
// the car keeps running (still on_course), so nothing reaches the ranking.
func TestUndoGoalWithinGrace(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "manual")
	t0 := time.Now().UnixMilli() * 1000
	row := launchManual(t, srv, queueID, t0)

	if err := srv.course.finishCar(row, t0+5_000*1000, "manual"); err != nil {
		t.Fatalf("finishCar: %v", err)
	}
	if err := srv.course.undoGoal(queueID); err != nil {
		t.Fatalf("undoGoal: %v", err)
	}

	// Log hard-deleted, still on course, nothing ranked, grace window gone.
	logs, _, _ := srv.Store.ListLogs(10, 0)
	if len(logs) != 0 {
		t.Fatalf("expected log removed by undo-goal, got %d", len(logs))
	}
	qrow, _, _ := srv.Store.GetQueueRow(queueID)
	if qrow.Status != "on_course" || qrow.TStartUS == nil {
		t.Fatalf("after undo-goal want RUNNING on_course, got status=%q tStart=%v", qrow.Status, qrow.TStartUS)
	}
	if srv.course.isPending(queueID) {
		t.Fatal("still pending after undo-goal")
	}

	// A later real timer fire for this queue id must be a harmless no-op.
	srv.course.confirm(queueID)
	if qrow, _, _ := srv.Store.GetQueueRow(queueID); qrow.Status != "on_course" {
		t.Fatalf("confirm after undo-goal changed status to %q", qrow.Status)
	}
	if n := rankingRowCount(t, srv); n != 0 {
		t.Fatalf("ranking rows = %d, want 0 after undo-goal", n)
	}
}

// TestUndoStart covers undo-start: the car leaves the course and returns to
// the waiting queue with PT reset.
func TestUndoStart(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "manual")
	t0 := time.Now().UnixMilli() * 1000
	row := launchManual(t, srv, queueID, t0)

	if _, err := srv.Store.SetPT(queueID, 2); err != nil {
		t.Fatalf("SetPT: %v", err)
	}
	row, _, _ = srv.Store.GetQueueRow(queueID)

	if err := srv.course.undoStart(row); err != nil {
		t.Fatalf("undoStart: %v", err)
	}
	qrow, _, _ := srv.Store.GetQueueRow(queueID)
	if qrow.Status != "waiting" {
		t.Fatalf("status = %q, want waiting", qrow.Status)
	}
	if qrow.TStartUS != nil {
		t.Fatal("t_start should be cleared by undo-start")
	}
	if qrow.PTCount != 0 {
		t.Fatalf("pt_count = %d, want 0 after undo-start", qrow.PTCount)
	}
}

// TestPTGuard confirms PT cannot be driven below zero through the store guard
// the course handler relies on.
func TestPTGuard(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "manual")
	launchManual(t, srv, queueID, time.Now().UnixMilli()*1000)

	if _, err := srv.Store.SetPT(queueID, -1); err == nil {
		t.Fatal("expected error decrementing PT below zero")
	}
	qrow, _, _ := srv.Store.GetQueueRow(queueID)
	if qrow.PTCount != 0 {
		t.Fatalf("pt_count = %d, want 0", qrow.PTCount)
	}
}
