package web

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// TestNoStorePages covers the withCacheControl default: HTML pages served
// through Routes() must carry "Cache-Control: no-store" so a Cloudflare-style
// proxy (or the browser) never caches an auth-bearing page.
func TestNoStorePages(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET /: Cache-Control = %q, want %q", got, "no-store")
	}
}

// TestNoStoreJSONAPIs covers the same default for a JSON API route.
func TestNoStoreJSONAPIs(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodGet, "/api/drivers", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/drivers: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET /api/drivers: Cache-Control = %q, want %q", got, "no-store")
	}
}

// TestIconNoCache covers the icon endpoints' opt-out: they are
// ETag-revalidated on every request (the icon is user-editable), not
// no-store'd, so a client always ends up sending If-None-Match instead of
// silently reusing a stale image.
func TestIconNoCache(t *testing.T) {
	srv, _, driverID, vehicleID := newTestServer(t, "sensor")

	if err := srv.Store.SetIcon(driverID, []byte("fake-jpeg-driver")); err != nil {
		t.Fatalf("SetIcon: %v", err)
	}
	if err := srv.Store.SetVehicleIcon(vehicleID, []byte("fake-jpeg-vehicle")); err != nil {
		t.Fatalf("SetVehicleIcon: %v", err)
	}

	for _, tc := range []struct {
		name string
		path string
	}{
		{"driver", "/api/drivers/" + strconv.FormatInt(driverID, 10) + "/icon"},
		{"vehicle", "/api/vehicles/" + strconv.FormatInt(vehicleID, 10) + "/icon"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200; body=%s", tc.path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
				t.Errorf("GET %s: Cache-Control = %q, want %q", tc.path, got, "no-cache")
			}
			if rec.Header().Get("ETag") == "" {
				t.Errorf("GET %s: ETag header missing", tc.path)
			}
		})
	}
}
