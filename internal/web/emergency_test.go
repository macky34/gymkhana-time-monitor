package web

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestEmergencyAdmin_AllowedUserManagementRoutes covers the routes the
// emergency identity exists for (see emergencyDriver / withUserAdmin): it can
// create a new user and then promote that user to admin, both reachable
// through withUserAdmin instead of withAdmin.
func TestEmergencyAdmin_AllowedUserManagementRoutes(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	classes, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(classes) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	cookie := &http.Cookie{Name: "tm_session", Value: srv.emergencyToken}

	createBody, _ := json.Marshal(map[string]any{"name": "新規ユーザ", "driver_class_id": classes[0].ID})
	createReq := httptest.NewRequest(http.MethodPost, "/api/admin/users", strings.NewReader(string(createBody)))
	createReq.AddCookie(cookie)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("POST /api/admin/users with emergency session: status = %d, want 200; body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		DriverID int64  `json:"driver_id"`
		LoginURL string `json:"login_url"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("create response: %v", err)
	}
	if created.DriverID == 0 || created.LoginURL == "" {
		t.Fatalf("create response = %+v, want non-zero driver_id and non-empty login_url", created)
	}

	roleBody, _ := json.Marshal(map[string]string{"role": "admin"})
	roleReq := httptest.NewRequest(http.MethodPut, "/api/admin/users/"+itoa(created.DriverID)+"/role", strings.NewReader(string(roleBody)))
	roleReq.AddCookie(cookie)
	roleReq.Header.Set("Content-Type", "application/json")
	roleRec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(roleRec, roleReq)
	if roleRec.Code != http.StatusOK {
		t.Fatalf("PUT /api/admin/users/%d/role with emergency session: status = %d, want 200; body=%s", created.DriverID, roleRec.Code, roleRec.Body.String())
	}
}

// TestEmergencyAdmin_ForbiddenRoutes covers the default-deny: every admin
// route other than the four user-management ones must reject the emergency
// session with 403 via withAdmin's isEmergency check, regardless of what the
// route actually does. Request bodies are placeholders - the 403 must happen
// before the handler ever parses one.
func TestEmergencyAdmin_ForbiddenRoutes(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	cookie := &http.Cookie{Name: "tm_session", Value: srv.emergencyToken}

	cases := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPut, "/api/admin/users/" + itoa(driverID), `{"name":"x","driver_class_id":1}`},
		{http.MethodPost, "/api/admin/users/" + itoa(driverID) + "/icon", `{}`},
		{http.MethodPost, "/api/admin/vehicles", `{}`},
		{http.MethodPut, "/api/admin/settings", `{}`},
		{http.MethodGet, "/api/admin/logs", ""},
		{http.MethodPost, "/api/admin/course", `{}`},
		{http.MethodPost, "/api/admin/course/adopt-orphan", `{"orphan_id":1}`},
		{http.MethodDelete, "/api/admin/course/orphan-runs/1", ""},
		{http.MethodDelete, "/api/admin/orphans/1", ""},
		{http.MethodDelete, "/api/admin/orphans", ""},
		{http.MethodGet, "/api/admin/export", ""},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.AddCookie(cookie)
		if c.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with emergency session: status = %d, want 403; body=%s", c.method, c.path, rec.Code, rec.Body.String())
		}
	}
}

// TestEmergencyAdmin_DoesNotAffectNormalAdmin covers that a real admin
// session is unaffected by the emergency restrictions: a route that 403s for
// the emergency session must behave normally for a real admin.
func TestEmergencyAdmin_DoesNotAffectNormalAdmin(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	cookie := &http.Cookie{Name: "tm_session", Value: "tok-admin"} // seeded admin driver's token, see newTestServer

	req := httptest.NewRequest(http.MethodGet, "/api/admin/logs", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/logs with normal admin session: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestEmergencyAdmin_HTMLHidesRestrictedTabs covers that admin.html hides the
// tabs the emergency session cannot use (server-rendered, via PageData.
// IsEmergency), while a normal admin session still sees the full tab bar.
func TestEmergencyAdmin_HTMLHidesRestrictedTabs(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	fetch := func(cookieVal string) string {
		req := httptest.NewRequest(http.MethodGet, "/admin", nil)
		req.AddCookie(&http.Cookie{Name: "tm_session", Value: cookieVal})
		rec := httptest.NewRecorder()
		srv.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /admin: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	emergencyHTML := fetch(srv.emergencyToken)
	if strings.Contains(emergencyHTML, `data-p="a-set"`) {
		t.Error("emergency /admin HTML contains data-p=\"a-set\", want it hidden")
	}
	if !strings.Contains(emergencyHTML, `data-p="a-usr"`) {
		t.Error("emergency /admin HTML is missing data-p=\"a-usr\"")
	}

	adminHTML := fetch("tok-admin")
	if !strings.Contains(adminHTML, `data-p="a-set"`) {
		t.Error("normal admin /admin HTML is missing data-p=\"a-set\"")
	}
	if !strings.Contains(adminHTML, `data-p="a-usr"`) {
		t.Error("normal admin /admin HTML is missing data-p=\"a-usr\"")
	}
}

// TestEmergencyAdmin_SSEOrphanTopicExcluded covers the SSE authorization
// change: Hub.Handler (internal/sse) has no connection-level admin gate at
// all - its isAdmin callback (the func literal passed to s.Hub.Handler in
// Routes()) only decides whether the request-scoped "orphan" topic
// subscribes. So a request the callback rejects still gets a normal 200
// SSE response; it just never receives anything published to "orphan". That
// callback now also excludes the emergency identity (d.Role == "admin" is
// true for it, so without the added isEmergency check it would have kept
// seeing this admin-only topic). This proves the callback change: an orphan
// item published before either session connects reaches a normal admin's
// stream but not the emergency session's.
func TestEmergencyAdmin_SSEOrphanTopicExcluded(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	srv.Hub.Publish(sse.TopicOrphan, []byte(`{"items":[{"kind":"orphan_start","detail":"test"}]}`))

	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	// readsOrphanEvent connects to /api/stream?topics=orphan with the given
	// session cookie and reports the HTTP status plus whether an "orphan" SSE
	// event arrives within a short window. Decoding is done by hand with
	// DisableCompression + compress/gzip rather than relying on net/http's
	// own transparent-gzip Response.Body wrapper: that wrapper's Read/Close
	// share an internal lock (net/http/transport.go's gzipReader), and
	// closing resp.Body from this goroutine while the scanner goroutine below
	// is still blocked mid-Read deadlocks on it. A raw, uncompressed-by-
	// transport body's Close() safely unblocks a concurrent Read instead.
	readsOrphanEvent := func(cookieVal string) (status int, gotOrphan bool) {
		req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/stream?topics=orphan", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		req.AddCookie(&http.Cookie{Name: "tm_session", Value: cookieVal})
		client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("Do: %v", err)
		}
		defer resp.Body.Close()
		status = resp.StatusCode

		lineCh := make(chan string, 1)
		go func() {
			gz, err := gzip.NewReader(resp.Body)
			if err != nil {
				return // body closed (timeout) before any gzip data ever arrived
			}
			sc := bufio.NewScanner(gz)
			for sc.Scan() {
				if line := sc.Text(); strings.HasPrefix(line, "event:") {
					lineCh <- line
					return
				}
			}
		}()
		select {
		case line := <-lineCh:
			gotOrphan = strings.Contains(line, "orphan")
		case <-time.After(500 * time.Millisecond):
			gotOrphan = false
		}
		return
	}

	status, got := readsOrphanEvent(srv.emergencyToken)
	if status != http.StatusOK {
		t.Fatalf("emergency GET /api/stream: status = %d, want 200 (Hub.Handler has no connection-level reject, see comment above)", status)
	}
	if got {
		t.Error("emergency session received the admin-only orphan SSE topic, want it excluded")
	}

	status2, got2 := readsOrphanEvent("tok-admin")
	if status2 != http.StatusOK {
		t.Fatalf("normal admin GET /api/stream: status = %d, want 200", status2)
	}
	if !got2 {
		t.Error("normal admin session did not receive the orphan SSE topic, want it included")
	}
}
