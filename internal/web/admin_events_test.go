package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"timemon/internal/store"
)

// callAdminEvents drives one of the withAdmin-wrapped admin_events.go
// handlers directly (bypassing Routes()/withCSRFGuard/withAdmin, same style
// as course_test.go's direct courseManager calls) with a JSON body, and
// returns the recorded response.
func callAdminEvents(t *testing.T, fn func(w http.ResponseWriter, r *http.Request, d store.Driver), method, path string, body any, admin store.Driver) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	fn(rec, req, admin)
	return rec
}

// TestAdminEventCreateConflict covers POST /api/admin/events: rejected with
// 409 while an event is already active (newTestServer seeds one), then
// succeeds once that event is closed, defaulting from defaults.json when
// copy_from_last is false.
func TestAdminEventCreateConflict(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	rec := callAdminEvents(t, srv.handleAdminEventCreate, http.MethodPost, "/api/admin/events",
		map[string]any{"name": "Second Event", "copy_from_last": false}, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create while active: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	active, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}
	if err := srv.Store.CloseEvent(active.ID); err != nil {
		t.Fatalf("CloseEvent: %v", err)
	}

	rec = callAdminEvents(t, srv.handleAdminEventCreate, http.MethodPost, "/api/admin/events",
		map[string]any{"name": "Second Event", "copy_from_last": false}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("create after close: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	newActive, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent after create: ok=%v err=%v", ok, err)
	}
	if newActive.EventName != "Second Event" {
		t.Errorf("new active event name = %q, want %q", newActive.EventName, "Second Event")
	}
	if newActive.TimingMode == "" {
		t.Errorf("new event TimingMode empty, want a value from defaults.json")
	}
}

// TestAdminSettingsGetNoActiveEvent covers GET /api/admin/settings once
// every event has been closed: stage 2 must report {"event": null} with a
// plain 200, not a 404/500/409, so the admin UI can tell "no active event"
// apart from a real failure.
func TestAdminSettingsGetNoActiveEvent(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	active, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}
	if err := srv.Store.CloseEvent(active.ID); err != nil {
		t.Fatalf("CloseEvent: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminSettingsGet(rec, req, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Event *int64 `json:"event"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v (body=%s)", err, rec.Body.String())
	}
	if body.Event != nil {
		t.Errorf(`body.event = %v, want null ({"event":null})`, *body.Event)
	}
}

// TestAdminEventCloseRejectsOnCourse covers POST /api/admin/events/{id}/close:
// rejected with 409 while the seeded queue row is on_course, then succeeds
// (and cancels any waiting rows) once it is not.
func TestAdminEventCloseRejectsOnCourse(t *testing.T) {
	srv, queueID, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	active, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}

	if err := srv.Store.SetQueueStatus(queueID, "on_course"); err != nil {
		t.Fatalf("SetQueueStatus: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/admin/events/"+strconv.FormatInt(active.ID, 10)+"/close", nil)
	req.SetPathValue("id", strconv.FormatInt(active.ID, 10))
	rec := httptest.NewRecorder()
	srv.handleAdminEventClose(rec, req, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("close with car on_course: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	// Back to waiting, plus a second untouched waiting row, then close for real.
	if err := srv.Store.SetQueueStatus(queueID, "waiting"); err != nil {
		t.Fatalf("SetQueueStatus(back to waiting): %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/admin/events/"+strconv.FormatInt(active.ID, 10)+"/close", nil)
	req.SetPathValue("id", strconv.FormatInt(active.ID, 10))
	rec = httptest.NewRecorder()
	srv.handleAdminEventClose(rec, req, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("close: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	closed, ok, err := srv.Store.GetEvent(active.ID)
	if err != nil || !ok {
		t.Fatalf("GetEvent: ok=%v err=%v", ok, err)
	}
	if closed.Status != "closed" {
		t.Errorf("event status = %q, want closed", closed.Status)
	}
	qrow, ok, err := srv.Store.GetQueueRow(queueID)
	if err != nil || !ok {
		t.Fatalf("GetQueueRow: ok=%v err=%v", ok, err)
	}
	if qrow.Status != "canceled" {
		t.Errorf("formerly-waiting queue row status = %q, want canceled", qrow.Status)
	}
}
