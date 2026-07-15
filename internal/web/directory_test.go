package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"timemon/internal/sse"
)

// directoryRev reads the current "directory" SSE snapshot's rev field (0 if
// nothing has been published to that topic yet).
func directoryRev(t *testing.T, srv *Server) int64 {
	t.Helper()
	data, ok := srv.Hub.Snapshot(sse.TopicDirectory)
	if !ok {
		return 0
	}
	var body struct {
		Rev int64 `json:"rev"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("unmarshal directory snapshot: %v (data=%s)", err, data)
	}
	return body.Rev
}

// TestAdminUserCreatePublishesDirectory covers the "directory" SSE topic
// added so the admin page knows when to refetch /api/admin/users and
// /api/vehicles: creating a user through the admin API must bump its rev.
func TestAdminUserCreatePublishesDirectory(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}

	before := directoryRev(t, srv)

	rec := callAdminEvents(t, srv.handleAdminUserCreate, http.MethodPost, "/api/admin/users",
		map[string]any{"name": "新規ドライバー", "driver_class_id": driverClasses[0].ID}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("create: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	after := directoryRev(t, srv)
	if after <= before {
		t.Fatalf("directory rev = %d, want > %d after admin user create", after, before)
	}
}
