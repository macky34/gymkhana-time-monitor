package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestTokenLogin_PersistentCookie verifies that logging in issues a
// persistent tm_session cookie (MaxAge set) rather than a browser-session
// cookie, so drivers survive a browser restart without re-scanning their QR.
func TestTokenLogin_PersistentCookie(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodGet, "/a/tok-admin", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /a/tok-admin: status = %d, want 302; body=%s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(t, rec)
	if cookie.MaxAge != sessionCookieMaxAge {
		t.Errorf("tm_session Max-Age = %d, want %d (persistent cookie)", cookie.MaxAge, sessionCookieMaxAge)
	}
}

// TestLogout verifies POST /api/logout returns 204 with an expired tm_session
// cookie (the browser-side delete), and that it also works without any
// session cookie (logging out twice must not error).
func TestLogout(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	req.AddCookie(&http.Cookie{Name: "tm_session", Value: "tok-admin"})
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("POST /api/logout: status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(t, rec)
	if cookie.MaxAge >= 0 || cookie.Value != "" {
		t.Errorf("logout cookie = {Value:%q MaxAge:%d}, want empty value with MaxAge<0", cookie.Value, cookie.MaxAge)
	}

	req2 := httptest.NewRequest(http.MethodPost, "/api/logout", nil)
	rec2 := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNoContent {
		t.Errorf("POST /api/logout without cookie: status = %d, want 204", rec2.Code)
	}
}
