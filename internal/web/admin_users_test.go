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

// callAdminUsersByID drives an {id}-path handler directly (bypassing
// Routes()), same style as admin_events_test.go's
// TestAdminEventCloseRejectsOnCourse: httptest.NewRequest does not route
// through the mux, so the "id" path value is set by hand before calling fn.
// Reused by admin_vehicles_test.go, icon_test.go and my_test.go for the same
// reason (all are {id}-path handlers taking a store.Driver caller).
func callAdminUsersByID(t *testing.T, fn func(w http.ResponseWriter, r *http.Request, d store.Driver), method, path string, id int64, body any, caller store.Driver) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.SetPathValue("id", strconv.FormatInt(id, 10))
	rec := httptest.NewRecorder()
	fn(rec, req, caller)
	return rec
}

// TestAdminUserCreate covers POST /api/admin/users: the returned login_url
// must embed the actual token persisted for the new driver.
func TestAdminUserCreate(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}

	rec := callAdminEvents(t, srv.handleAdminUserCreate, http.MethodPost, "/api/admin/users",
		map[string]any{"name": "新規ユーザー", "driver_class_id": driverClasses[0].ID}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[struct {
		DriverID int64  `json:"driver_id"`
		LoginURL string `json:"login_url"`
	}](t, rec.Body.Bytes())

	created, ok, err := srv.Store.GetDriver(out.DriverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver(new): ok=%v err=%v", ok, err)
	}
	want := srv.BaseURL + "/a/" + created.Token
	if out.LoginURL != want {
		t.Errorf("login_url = %q, want %q", out.LoginURL, want)
	}
}

// TestAdminUsersListIncludesIconRev covers GET /api/admin/users: uploaded
// icon bytes must actually thread through to icon_rev in the response, not
// just leave has_icon true with a zero-value IconRev left unset by mistake.
func TestAdminUsersListIncludesIconRev(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if err := srv.Store.SetIcon(driverID, []byte{0x01}); err != nil {
		t.Fatalf("SetIcon: %v", err)
	}
	admin, ok, err = srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver (after SetIcon): ok=%v err=%v", ok, err)
	}

	rec := callAdminEvents(t, srv.handleAdminUsersList, http.MethodGet, "/api/admin/users", nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/users: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[struct {
		Users []struct {
			ID      int64 `json:"id"`
			HasIcon bool  `json:"has_icon"`
			IconRev int64 `json:"icon_rev"`
		} `json:"users"`
	}](t, rec.Body.Bytes())

	var found bool
	for _, u := range out.Users {
		if u.ID != driverID {
			continue
		}
		found = true
		if !u.HasIcon || u.IconRev != admin.IconRev {
			t.Errorf("GET /api/admin/users user %d = %+v, want has_icon=true icon_rev=%d", driverID, u, admin.IconRev)
		}
	}
	if !found {
		t.Fatalf("GET /api/admin/users: driver %d not found in %+v", driverID, out.Users)
	}
}

// TestAdminUserUpdate covers PUT /api/admin/users/{id}: rename and driver
// class change both persist. newTestServer only seeds a single driver class
// ("現役"), so a second class is added here (via a second SeedEvent, after
// closing the first event - class_defs are event-independent/global) to
// actually exercise a class *change* rather than a same-value round-trip.
func TestAdminUserUpdate(t *testing.T) {
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
	second := active
	second.EventName = "second"
	if err := srv.Store.SeedEvent(second, []string{"新区分"}, nil); err != nil {
		t.Fatalf("SeedEvent: %v", err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) < 2 {
		t.Fatalf("ListClassDefs driver: got %d classes, want >=2 (err=%v)", len(driverClasses), err)
	}

	target, err := srv.Store.CreateDriver("旧名前", driverClasses[0].ID, "tok-update-target", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}

	rec := callAdminUsersByID(t, srv.handleAdminUserUpdate, http.MethodPut, "/api/admin/users/"+itoa(target),
		target, map[string]any{"name": "新名前", "driver_class_id": driverClasses[1].ID}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	updated, ok, err := srv.Store.GetDriver(target)
	if err != nil || !ok {
		t.Fatalf("GetDriver(updated): ok=%v err=%v", ok, err)
	}
	if updated.Name != "新名前" {
		t.Errorf("name = %q, want %q", updated.Name, "新名前")
	}
	if updated.DriverClassID != driverClasses[1].ID {
		t.Errorf("driver_class_id = %d, want %d", updated.DriverClassID, driverClasses[1].ID)
	}
}

// TestAdminUserReissue covers POST /api/admin/users/{id}/reissue: the old
// token must stop resolving and the returned login_url must embed the new
// one.
func TestAdminUserReissue(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	target, err := srv.Store.CreateDriver("再発行対象", driverClasses[0].ID, "tok-old", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}

	rec := callAdminUsersByID(t, srv.handleAdminUserReissue, http.MethodPost, "/api/admin/users/"+itoa(target)+"/reissue",
		target, nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[struct {
		LoginURL string `json:"login_url"`
	}](t, rec.Body.Bytes())

	if _, ok, err := srv.Store.GetDriverByToken("tok-old"); err != nil || ok {
		t.Fatalf("GetDriverByToken(old token): ok=%v err=%v, want ok=false after reissue", ok, err)
	}
	updated, ok, err := srv.Store.GetDriver(target)
	if err != nil || !ok {
		t.Fatalf("GetDriver(target): ok=%v err=%v", ok, err)
	}
	if updated.Token == "tok-old" {
		t.Fatal("token unchanged after reissue")
	}
	want := srv.BaseURL + "/a/" + updated.Token
	if out.LoginURL != want {
		t.Errorf("login_url = %q, want %q", out.LoginURL, want)
	}
}

// TestAdminRegisterLink covers GET /api/admin/register-link: it must return
// a fixed /entry URL (no token) plus a non-empty QR PNG for that same
// URL. Emergency-session rejection is covered separately by
// TestEmergencyAdmin_ForbiddenRoutes (this handler is on withAdmin, not
// withUserAdmin - see its own comment for why).
func TestAdminRegisterLink(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/admin/register-link", nil)
	rec := httptest.NewRecorder()
	srv.handleAdminRegisterLink(rec, req, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[struct {
		RegisterURL string `json:"register_url"`
		QRPNGB64    string `json:"qr_png_b64"`
	}](t, rec.Body.Bytes())

	want := srv.BaseURL + "/entry"
	if out.RegisterURL != want {
		t.Errorf("register_url = %q, want %q", out.RegisterURL, want)
	}
	if out.QRPNGB64 == "" {
		t.Error("qr_png_b64 is empty")
	}
}

// TestAdminUserDeleteRemovesFromListAndRevokesLogin covers DELETE
// /api/admin/users/{id}: the logically-deleted user disappears from GET
// /api/admin/users and its login token stops resolving (GetDriverByToken
// filters is_deleted, unlike GetDriver which callers use for historical
// name lookups).
func TestAdminUserDeleteRemovesFromListAndRevokesLogin(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	target, err := srv.Store.CreateDriver("削除対象", driverClasses[0].ID, "tok-delete-target", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}

	rec := callAdminUsersByID(t, srv.handleAdminUserDelete, http.MethodDelete, "/api/admin/users/"+itoa(target),
		target, nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	listRec := callAdminEvents(t, srv.handleAdminUsersList, http.MethodGet, "/api/admin/users", nil, admin)
	list := decodeJSON[struct {
		Users []struct {
			ID int64 `json:"id"`
		} `json:"users"`
	}](t, listRec.Body.Bytes())
	for _, u := range list.Users {
		if u.ID == target {
			t.Fatalf("deleted user %d still present in GET /api/admin/users list", target)
		}
	}

	if _, ok, err := srv.Store.GetDriverByToken("tok-delete-target"); err != nil || ok {
		t.Fatalf("GetDriverByToken(deleted user's token): ok=%v err=%v, want ok=false", ok, err)
	}
}

// TestAdminUserDeleteRejectsSelf covers DELETE /api/admin/users/{id}: an
// admin cannot delete their own account (would strand the current session
// with no sane recovery), regardless of whether other admins exist.
func TestAdminUserDeleteRejectsSelf(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	// A second admin exists, so this is not also a last-admin rejection -
	// isolates the self-delete guard.
	second, err := srv.Store.CreateDriver("二人目", driverClasses[0].ID, "tok-second-admin", "admin")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	_ = second

	rec := callAdminUsersByID(t, srv.handleAdminUserDelete, http.MethodDelete, "/api/admin/users/"+itoa(driverID),
		driverID, nil, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("self-delete: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	stillThere, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok || stillThere.Role != "admin" {
		t.Fatalf("GetDriver(self): ok=%v err=%v driver=%+v, want unaffected admin", ok, err, stillThere)
	}
}

// TestAdminUserDeleteRejectsLastAdmin covers DELETE /api/admin/users/{id}:
// deleting the sole remaining admin (a different one from the caller) is
// rejected with 409, and succeeds once a second admin exists.
func TestAdminUserDeleteRejectsLastAdmin(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}

	// Sole admin (driverID) tries to delete itself - covered by
	// TestAdminUserDeleteRejectsSelf, so instead promote a second driver to
	// admin, then have the original delete the second one, dropping the
	// admin count from 2 to 1: that must succeed. Then create a third
	// non-admin target and confirm a would-be last-admin scenario is
	// rejected by demoting back down to a single admin and attempting to
	// delete that sole admin from a *different* admin session.
	second, err := srv.Store.CreateDriver("二人目", driverClasses[0].ID, "tok-second", "admin")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	secondDriver, ok, err := srv.Store.GetDriver(second)
	if err != nil || !ok {
		t.Fatalf("GetDriver(second): ok=%v err=%v", ok, err)
	}

	// With two admins, the original can delete the second one.
	rec := callAdminUsersByID(t, srv.handleAdminUserDelete, http.MethodDelete, "/api/admin/users/"+itoa(second),
		second, nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete second admin (two exist): status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Recreate a second admin, then try to have IT delete the original
	// (still the caller in this call) while both are admins: that's fine.
	// The actually-interesting case - deleting the sole admin - is only
	// reachable by a non-self caller, so simulate that with the (deleted)
	// secondDriver struct as the "caller" acting on driverID while driverID
	// is the sole active admin.
	rec2 := callAdminUsersByID(t, srv.handleAdminUserDelete, http.MethodDelete, "/api/admin/users/"+itoa(driverID),
		driverID, nil, secondDriver)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("delete sole remaining admin: status = %d, want 409; body=%s", rec2.Code, rec2.Body.String())
	}
	stillThere, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok || stillThere.Role != "admin" {
		t.Fatalf("GetDriver(sole admin): ok=%v err=%v driver=%+v, want unaffected admin", ok, err, stillThere)
	}
}

// TestAdminUserRoleLastAdminConflict covers PUT /api/admin/users/{id}/role:
// demoting the sole remaining admin is rejected with 409 and leaves the role
// untouched; once a second admin exists, the same demotion succeeds.
func TestAdminUserRoleLastAdminConflict(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	rec := callAdminUsersByID(t, srv.handleAdminUserRole, http.MethodPut, "/api/admin/users/"+itoa(driverID)+"/role",
		driverID, map[string]any{"role": "user"}, admin)
	if rec.Code != http.StatusConflict {
		t.Fatalf("demote sole admin: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	stillAdmin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if stillAdmin.Role != "admin" {
		t.Errorf("role = %q, want unchanged admin after rejected demotion", stillAdmin.Role)
	}

	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	second, err := srv.Store.CreateDriver("二人目", driverClasses[0].ID, "tok-second", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	rec = callAdminUsersByID(t, srv.handleAdminUserRole, http.MethodPut, "/api/admin/users/"+itoa(second)+"/role",
		second, map[string]any{"role": "admin"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote second driver: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec = callAdminUsersByID(t, srv.handleAdminUserRole, http.MethodPut, "/api/admin/users/"+itoa(driverID)+"/role",
		driverID, map[string]any{"role": "user"}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("demote original (no longer sole admin): status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	demoted, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if demoted.Role != "user" {
		t.Errorf("role = %q, want user after successful demotion", demoted.Role)
	}
}
