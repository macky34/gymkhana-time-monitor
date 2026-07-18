package web

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"timemon/internal/timing"
)

// emptyWaitingQueue cancels newTestServer's single seeded waiting row so the
// orphan-pairing paths below (which require no READY/RUNNING car) can be
// exercised without a queue row shadowing them.
func emptyWaitingQueue(t *testing.T, srv *Server, queueID int64) {
	t.Helper()
	if err := srv.Store.SetQueueStatus(queueID, "canceled"); err != nil {
		t.Fatalf("SetQueueStatus(canceled): %v", err)
	}
}

// (1) start+goal with an empty queue produces an unassigned log with an
// accurate raw_ms, and that log is excluded from the ranking.
func TestOrphanPairingProducesUnassignedLog(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)

	const tStart = int64(1_700_000_000_000_000)
	const elapsedMS = 12_345
	if err := srv.course.SensorStart(tStart); err != nil {
		t.Fatalf("SensorStart: %v", err)
	}
	if err := srv.course.SensorGoal(tStart + elapsedMS*1000); err != nil {
		t.Fatalf("SensorGoal: %v", err)
	}

	ev, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}
	logs, total, err := srv.Store.ListLogs(ev.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("logs total = %d, want 1", total)
	}
	l := logs[0]
	if l.DriverID != nil || l.VehicleID != nil {
		t.Fatalf("expected unassigned log, got driver=%v vehicle=%v", l.DriverID, l.VehicleID)
	}
	if l.RawMS != elapsedMS {
		t.Fatalf("raw_ms = %d, want %d", l.RawMS, elapsedMS)
	}
	if l.Source != "sensor" {
		t.Fatalf("source = %q, want sensor", l.Source)
	}

	if n := rankingRowCount(t, srv); n != 0 {
		t.Fatalf("ranking rows = %d, want 0 for an unassigned log", n)
	}

	// The orphan-run FIFO should be empty again (consumed by the goal), and
	// no unresolved warning should reference a still-missing log id: the
	// unassigned_log item should exist and carry this log's id.
	if runs := srv.course.orphanRunsSnapshot(); len(runs) != 0 {
		t.Fatalf("orphan runs = %d, want 0 after pairing", len(runs))
	}
	items := srv.orphans.snapshot()
	if len(items) != 1 {
		t.Fatalf("orphan items = %d, want 1", len(items))
	}
	if items[0].Kind != orphanKindUnassignedLog {
		t.Fatalf("orphan item kind = %q, want %q", items[0].Kind, orphanKindUnassignedLog)
	}
	if items[0].LogID == nil || *items[0].LogID != l.ID {
		t.Fatalf("orphan item log_id = %v, want %d", items[0].LogID, l.ID)
	}
}

// (2) a goal trigger with no running car and no queued orphan run still
// reports ErrNoTarget (unchanged behavior).
func TestSensorGoalNoTargetNoOrphanRun(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)

	err := srv.course.SensorGoal(1_700_000_000_000_000)
	if !errors.Is(err, timing.ErrNoTarget) {
		t.Fatalf("SensorGoal err = %v, want ErrNoTarget", err)
	}
}

// (3) a lone start trigger (no goal yet) must not raise any orphan warning
// item - only the orphan-run FIFO grows.
func TestSensorStartAloneDoesNotWarn(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)

	if err := srv.course.SensorStart(1_700_000_000_000_000); err != nil {
		t.Fatalf("SensorStart: %v", err)
	}

	if items := srv.orphans.snapshot(); len(items) != 0 {
		t.Fatalf("orphan items = %d, want 0 after a lone start trigger", len(items))
	}
	if runs := srv.course.orphanRunsSnapshot(); len(runs) != 1 {
		t.Fatalf("orphan runs = %d, want 1", len(runs))
	}
}

// (4) an orphan run that never gets a goal trigger within max_course_time_sec
// expires: a later goal trigger does not pair with it (ErrNoTarget), and an
// orphan_start_expired warning is recorded instead.
func TestOrphanRunExpires(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)

	ev, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}

	const tStart = int64(1_700_000_000_000_000)
	if err := srv.course.SensorStart(tStart); err != nil {
		t.Fatalf("SensorStart: %v", err)
	}

	tGoal := tStart + (int64(ev.MaxCourseTimeSec)+1)*1_000_000
	err = srv.course.SensorGoal(tGoal)
	if !errors.Is(err, timing.ErrNoTarget) {
		t.Fatalf("SensorGoal after expiry err = %v, want ErrNoTarget", err)
	}

	if runs := srv.course.orphanRunsSnapshot(); len(runs) != 0 {
		t.Fatalf("orphan runs = %d, want 0 (expired and pruned)", len(runs))
	}
	items := srv.orphans.snapshot()
	if len(items) != 1 || items[0].Kind != orphanKindStartExpired {
		t.Fatalf("orphan items = %+v, want 1 item of kind %q", items, orphanKindStartExpired)
	}
}

// (5) multiple queued orphan starts are paired FIFO (oldest first).
func TestOrphanRunFIFOOrder(t *testing.T) {
	srv, queueID, _, _ := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)

	t1 := int64(1_700_000_000_000_000)
	t2 := t1 + 5_000_000
	t3 := t1 + 9_000_000
	for _, ts := range []int64{t1, t2, t3} {
		if err := srv.course.SensorStart(ts); err != nil {
			t.Fatalf("SensorStart(%d): %v", ts, err)
		}
	}

	ev, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}

	// First goal must pair with t1.
	if err := srv.course.SensorGoal(t1 + 1_000_000); err != nil {
		t.Fatalf("SensorGoal 1: %v", err)
	}
	logs, _, err := srv.Store.ListLogs(ev.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(logs) != 1 || logs[0].RawMS != 1000 {
		t.Fatalf("after 1st goal: logs = %+v, want 1 log raw_ms=1000", logs)
	}

	// Second goal must pair with t2 (next oldest), not t3.
	if err := srv.course.SensorGoal(t2 + 2_000_000); err != nil {
		t.Fatalf("SensorGoal 2: %v", err)
	}
	logs, _, err = srv.Store.ListLogs(ev.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("after 2nd goal: %d logs, want 2", len(logs))
	}

	if runs := srv.course.orphanRunsSnapshot(); len(runs) != 1 || runs[0].TStartUS != t3 {
		t.Fatalf("orphan runs = %+v, want exactly t3=%d left", runs, t3)
	}
}

// (6) with a READY car actually waiting on course, sensor triggers keep
// pairing with the queue exactly as before (regression: orphan handling must
// not shadow the existing queue-car pairing path).
func TestSensorPairingWithReadyCarUnaffected(t *testing.T) {
	srv, queueID, driverID, vehicleID := newTestServer(t, "sensor")

	if err := srv.Store.SetQueueStatus(queueID, "on_course"); err != nil {
		t.Fatalf("SetQueueStatus: %v", err)
	}
	if err := srv.Store.SetStart(queueID, nil); err != nil {
		t.Fatalf("SetStart(nil): %v", err)
	}

	const tStart = int64(1_700_000_000_000_000)
	if err := srv.course.SensorStart(tStart); err != nil {
		t.Fatalf("SensorStart: %v", err)
	}

	row, ok, err := srv.Store.GetQueueRow(queueID)
	if err != nil || !ok {
		t.Fatalf("GetQueueRow: %v ok=%v", err, ok)
	}
	if row.TStartUS == nil || *row.TStartUS != tStart {
		t.Fatalf("queue row t_start = %v, want %d", row.TStartUS, tStart)
	}
	if runs := srv.course.orphanRunsSnapshot(); len(runs) != 0 {
		t.Fatalf("orphan runs = %d, want 0 (READY car should have absorbed the start)", len(runs))
	}

	if err := srv.course.SensorGoal(tStart + 3_000_000); err != nil {
		t.Fatalf("SensorGoal: %v", err)
	}
	if !srv.course.isPending(queueID) {
		t.Fatal("expected finish pending after goal pairs with the RUNNING queue car")
	}
	ev, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}
	logs, _, err := srv.Store.ListLogs(ev.ID, 10, 0)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if len(logs) != 1 || logs[0].DriverID == nil || *logs[0].DriverID != driverID || logs[0].VehicleID == nil || *logs[0].VehicleID != vehicleID {
		t.Fatalf("logs = %+v, want 1 assigned log for driver=%d vehicle=%d", logs, driverID, vehicleID)
	}

	// Let the finish grace-window timer fire before the test's t.Cleanup
	// closes the DB (same reasoning as course_test.go's
	// TestFinishGraceConfirm): otherwise confirm() races the DB close and
	// logs a harmless but noisy "database is closed" error.
	time.Sleep(120 * time.Millisecond)
}

// ---------------------------------------------------------------------
// admin APIs
// ---------------------------------------------------------------------

// (7) adopt-orphan: normal path pairs the head-of-waiting queue row with a
// queued orphan run's start timestamp.
func TestAdminCourseAdoptOrphan(t *testing.T) {
	srv, queueID, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	const tStart = int64(1_700_000_000_000_000)
	if err := srv.course.SensorStart(tStart); err != nil {
		t.Fatalf("SensorStart: %v", err)
	}
	runs := srv.course.orphanRunsSnapshot()
	if len(runs) != 1 {
		t.Fatalf("orphan runs = %d, want 1", len(runs))
	}
	orphanID := runs[0].ID

	rec := callAdminEvents(t, srv.handleAdminCourseAdoptOrphan, http.MethodPost, "/api/admin/course/adopt-orphan",
		map[string]any{"orphan_id": orphanID}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[struct {
		QueueID int64 `json:"queue_id"`
	}](t, rec.Body.Bytes())
	if out.QueueID != queueID {
		t.Fatalf("queue_id = %d, want %d", out.QueueID, queueID)
	}

	row, ok, err := srv.Store.GetQueueRow(queueID)
	if err != nil || !ok {
		t.Fatalf("GetQueueRow: %v ok=%v", err, ok)
	}
	if row.Status != "on_course" {
		t.Fatalf("status = %q, want on_course", row.Status)
	}
	if row.TStartUS == nil || *row.TStartUS != tStart {
		t.Fatalf("t_start = %v, want %d", row.TStartUS, tStart)
	}
	if runs := srv.course.orphanRunsSnapshot(); len(runs) != 0 {
		t.Fatalf("orphan runs = %d, want 0 after adoption", len(runs))
	}
}

// (7b) adopt-orphan: unknown orphan_id -> 409.
func TestAdminCourseAdoptOrphanUnknownID(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	rec := callAdminEvents(t, srv.handleAdminCourseAdoptOrphan, http.MethodPost, "/api/admin/course/adopt-orphan",
		map[string]any{"orphan_id": int64(99999)}, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// (7c) adopt-orphan: empty waiting queue -> 409 (same convention as
// handleAdminCourseLaunch's "queue is empty").
func TestAdminCourseAdoptOrphanEmptyQueue(t *testing.T) {
	srv, queueID, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	emptyWaitingQueue(t, srv, queueID)

	rec := callAdminEvents(t, srv.handleAdminCourseAdoptOrphan, http.MethodPost, "/api/admin/course/adopt-orphan",
		map[string]any{"orphan_id": int64(1)}, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// (7d) adopt-orphan: non-admin session -> 403 (routed through Routes() so
// withAdmin actually runs).
func TestAdminCourseAdoptOrphanForbiddenForNonAdmin(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs: %v", err)
	}
	userID, err := srv.Store.CreateDriver("一般ユーザー", driverClasses[0].ID, "tok-user", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	user, ok, err := srv.Store.GetDriver(userID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(map[string]any{"orphan_id": 1}); err != nil {
		t.Fatalf("encode body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/course/adopt-orphan", &buf)
	req.AddCookie(&http.Cookie{Name: "tm_session", Value: user.Token})
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// (8) dismiss-orphan-run: normal path discards a queued orphan run.
func TestAdminCourseDismissOrphanRun(t *testing.T) {
	srv, queueID, driverID, _ := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	if err := srv.course.SensorStart(1_700_000_000_000_000); err != nil {
		t.Fatalf("SensorStart: %v", err)
	}
	runs := srv.course.orphanRunsSnapshot()
	if len(runs) != 1 {
		t.Fatalf("orphan runs = %d, want 1", len(runs))
	}
	orphanID := runs[0].ID

	rec := callAdminUsersByID(t, srv.handleAdminCourseDismissOrphanRun, http.MethodDelete,
		"/api/admin/course/orphan-runs/"+itoa(orphanID), orphanID, nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if runs := srv.course.orphanRunsSnapshot(); len(runs) != 0 {
		t.Fatalf("orphan runs = %d, want 0 after dismiss", len(runs))
	}

	// Dismissing again (already consumed) -> 409.
	rec = callAdminUsersByID(t, srv.handleAdminCourseDismissOrphanRun, http.MethodDelete,
		"/api/admin/course/orphan-runs/"+itoa(orphanID), orphanID, nil, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 on second dismiss; body=%s", rec.Code, rec.Body.String())
	}
}

// (8b) orphan warning delete (individual) and clear (all).
func TestAdminOrphanDismissAndClear(t *testing.T) {
	srv, queueID, driverID, _ := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	srv.orphans.add("orphan_goal", "warning 1", nil)
	srv.orphans.add("orphan_goal", "warning 2", nil)
	items := srv.orphans.snapshot()
	if len(items) != 2 {
		t.Fatalf("orphan items = %d, want 2", len(items))
	}
	firstID := items[0].ID

	rec := callAdminUsersByID(t, srv.handleAdminOrphanDismiss, http.MethodDelete,
		"/api/admin/orphans/"+itoa(firstID), firstID, nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if items := srv.orphans.snapshot(); len(items) != 1 {
		t.Fatalf("orphan items = %d, want 1 after individual dismiss", len(items))
	}

	// Dismissing an id that no longer exists -> 404.
	rec = callAdminUsersByID(t, srv.handleAdminOrphanDismiss, http.MethodDelete,
		"/api/admin/orphans/"+itoa(firstID), firstID, nil, admin)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 on repeat dismiss; body=%s", rec.Code, rec.Body.String())
	}

	rec = callAdminEvents(t, srv.handleAdminOrphanClear, http.MethodDelete, "/api/admin/orphans", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if items := srv.orphans.snapshot(); len(items) != 0 {
		t.Fatalf("orphan items = %d, want 0 after clear", len(items))
	}
}

// (9) assigning a previously-unassigned (orphan-produced) log clears its
// orphan warning automatically.
func TestAdminLogAssignClearsOrphanWarning(t *testing.T) {
	srv, queueID, driverID, vehicleID := newTestServer(t, "sensor")
	emptyWaitingQueue(t, srv, queueID)
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	const tStart = int64(1_700_000_000_000_000)
	if err := srv.course.SensorStart(tStart); err != nil {
		t.Fatalf("SensorStart: %v", err)
	}
	if err := srv.course.SensorGoal(tStart + 8_000_000); err != nil {
		t.Fatalf("SensorGoal: %v", err)
	}

	items := srv.orphans.snapshot()
	if len(items) != 1 || items[0].LogID == nil {
		t.Fatalf("orphan items = %+v, want exactly 1 with a log_id", items)
	}
	logID := *items[0].LogID

	rec := callAdminUsersByID(t, srv.handleAdminLogAssign, http.MethodPut, "/api/admin/logs/"+itoa(logID),
		logID, map[string]any{"driver_id": driverID, "vehicle_id": vehicleID}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	if items := srv.orphans.snapshot(); len(items) != 0 {
		t.Fatalf("orphan items = %d, want 0 after assign", len(items))
	}

	l, ok, err := srv.Store.GetLog(logID)
	if err != nil || !ok {
		t.Fatalf("GetLog: ok=%v err=%v", ok, err)
	}
	if l.DriverID == nil || *l.DriverID != driverID || l.VehicleID == nil || *l.VehicleID != vehicleID {
		t.Fatalf("log = %+v, want assigned to driver=%d vehicle=%d", l, driverID, vehicleID)
	}
	if n := rankingRowCount(t, srv); n != 1 {
		t.Fatalf("ranking rows = %d, want 1 once assigned", n)
	}
}
