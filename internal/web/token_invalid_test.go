package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTokenLogin_InvalidTokenShowsJapanesePage covers GET /a/{token} for an
// unknown token: it must 404 like before, but now with a Japanese
// explanation page instead of Go's bare "404 page not found".
func TestTokenLogin_InvalidTokenShowsJapanesePage(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodGet, "/a/definitely-not-a-valid-token", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /a/{unknown token}: status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "このログインURLは無効です") {
		t.Errorf("GET /a/{unknown token}: body does not contain Japanese message; body=%s", rec.Body.String())
	}
}

// TestSetupPage_InvalidTokenShowsJapanesePage covers GET /setup?t=... with a
// wrong token on a freshly-seeded server (setup already done, so any token
// 404s) - same Japanese page, not a bare 404.
func TestSetupPage_InvalidTokenShowsJapanesePage(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodGet, "/setup?t=anything", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /setup?t=anything: status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "このセットアップURLは無効です") {
		t.Errorf("GET /setup?t=anything: body does not contain Japanese message; body=%s", rec.Body.String())
	}
}
