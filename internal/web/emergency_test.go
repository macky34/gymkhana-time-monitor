package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"timemon/internal/snapshot"
	"timemon/internal/sse"
	"timemon/internal/store"
)

// sessionCookieFrom extracts the tm_session cookie set on rec, failing the
// test if it is missing.
func sessionCookieFrom(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "tm_session" {
			return c
		}
	}
	t.Fatal("no tm_session cookie in response")
	return nil
}

// TestEmergencyAdmin_Login covers the happy path: even while real admins
// exist (they may all have lost their login URLs - the emergency token is
// unconditional), GET /a/{emergencyToken} logs in as the synthetic emergency
// admin and that session can reach the admin user-management API.
func TestEmergencyAdmin_Login(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	if srv.emergencyToken == "" {
		t.Fatal("srv.emergencyToken is empty, want non-empty for an already-seeded DB")
	}

	req := httptest.NewRequest(http.MethodGet, "/a/"+srv.emergencyToken, nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /a/{emergencyToken}: status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != "/admin" {
		t.Errorf("GET /a/{emergencyToken}: Location = %q, want %q", got, "/admin")
	}
	cookie := sessionCookieFrom(t, rec)
	if cookie.Value != srv.emergencyToken {
		t.Errorf("tm_session = %q, want %q", cookie.Value, srv.emergencyToken)
	}

	req2 := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	req2.AddCookie(cookie)
	rec2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/users with emergency session: status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
}

// TestEmergencyAdmin_RecoverLostAdminToken covers the intended recovery
// flow end-to-end: the sole admin has lost their login URL, so the operator
// logs in via the emergency URL from the startup log, reissues that admin's
// token through the normal admin API, and the returned login_url works as a
// real admin session. The emergency session itself keeps working throughout
// (it is unconditional; only a restart rotates it away).
func TestEmergencyAdmin_RecoverLostAdminToken(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")

	loginReq := httptest.NewRequest(http.MethodGet, "/a/"+srv.emergencyToken, nil)
	loginRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(loginRec, loginReq)
	cookie := sessionCookieFrom(t, loginRec)

	reissueReq := httptest.NewRequest(http.MethodPost, "/api/admin/users/"+itoa(driverID)+"/reissue", nil)
	reissueReq.AddCookie(cookie)
	reissueRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(reissueRec, reissueReq)
	if reissueRec.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/users/%d/reissue: status = %d, want 200; body=%s", driverID, reissueRec.Code, reissueRec.Body.String())
	}
	var reissued struct {
		LoginURL string `json:"login_url"`
	}
	if err := json.Unmarshal(reissueRec.Body.Bytes(), &reissued); err != nil {
		t.Fatalf("reissue response: %v", err)
	}
	newToken := reissued.LoginURL[strings.LastIndex(reissued.LoginURL, "/")+1:]

	realReq := httptest.NewRequest(http.MethodGet, "/a/"+newToken, nil)
	realRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(realRec, realReq)
	if realRec.Code != http.StatusFound {
		t.Fatalf("GET /a/{reissued token}: status = %d, want 302", realRec.Code)
	}
	realCookie := sessionCookieFrom(t, realRec)
	realUsersReq := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	realUsersReq.AddCookie(realCookie)
	realUsersRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(realUsersRec, realUsersReq)
	if realUsersRec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/users as reissued admin: status = %d, want 200; body=%s", realUsersRec.Code, realUsersRec.Body.String())
	}

	againReq := httptest.NewRequest(http.MethodGet, "/api/admin/users", nil)
	againReq.AddCookie(cookie)
	againRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(againRec, againReq)
	if againRec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/users with emergency session after reissue: status = %d, want 200; body=%s", againRec.Code, againRec.Body.String())
	}
}

// TestEmergencyAdmin_WrongToken404 covers the negative case: a token that
// matches neither a driver nor the emergency token stays a bare 404.
func TestEmergencyAdmin_WrongToken404(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodGet, "/a/definitely-not-a-valid-token", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /a/{unknown token}: status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestEmergencyAdmin_NotIssuedBeforeSetup covers that a brand-new,
// never-seeded database gets a setup token but no emergency token - the
// emergency path only makes sense once setup has established the concept of
// "the" admin roster.
func TestEmergencyAdmin_NotIssuedBeforeSetup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "unseeded.sqlite3")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	hub := sse.NewHub()
	snap := snapshot.New(st)
	srv, err := NewServer(st, hub, snap, "http://test")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	if srv.emergencyToken != "" {
		t.Errorf("emergencyToken = %q, want empty before setup", srv.emergencyToken)
	}
	if srv.setupToken == "" {
		t.Error("setupToken is empty, want non-empty before setup")
	}
}

// TestSetupTokenNotIssuedAfterSeed covers the potential-bug fix: once the DB
// has been seeded, /setup must 404 for any token - including once admins
// drop back to zero, which is exactly the state that used to make HasAdmin
// re-open /setup and risk a second SeedEvent.
func TestSetupTokenNotIssuedAfterSeed(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	if srv.setupToken != "" {
		t.Fatalf("setupToken = %q, want empty for an already-seeded DB", srv.setupToken)
	}
	if err := srv.Store.SetRole(driverID, "user"); err != nil {
		t.Fatalf("SetRole: %v", err)
	}

	for _, tok := range []string{"", "anything", srv.emergencyToken} {
		req := httptest.NewRequest(http.MethodGet, "/setup?t="+tok, nil)
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET /setup?t=%q: status = %d, want 404", tok, rec.Code)
		}
	}
}
