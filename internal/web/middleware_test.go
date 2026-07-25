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

// TestIconCacheControl covers the icon endpoints' Cache-Control split: a
// bare URL (no "?v=" cache-buster, or a malformed one — see isValidRev in
// icon.go) is ETag-revalidated on every request, not no-store'd, so a
// client always ends up sending If-None-Match instead of silently reusing
// a stale image — this matters for old clients, anyone hitting the URL
// directly, and any future output type that forgets to populate icon_rev
// (sending the literal string "undefined" as ?v=). A valid "?v=<int>" URL
// is content-addressed (the URL itself changes whenever icon_rev bumps —
// see setIcon in internal/store/helpers.go) so it is safe to cache
// long-term without revalidation, same policy as /static/.
func TestIconCacheControl(t *testing.T) {
	srv, _, driverID, vehicleID := newTestServer(t, "sensor")

	if err := srv.Store.SetIcon(driverID, []byte("fake-jpeg-driver")); err != nil {
		t.Fatalf("SetIcon: %v", err)
	}
	if err := srv.Store.SetVehicleIcon(vehicleID, []byte("fake-jpeg-vehicle")); err != nil {
		t.Fatalf("SetVehicleIcon: %v", err)
	}

	for _, tc := range []struct {
		name          string
		path          string
		wantCacheCtrl string
	}{
		{"driver bare URL", "/api/drivers/" + strconv.FormatInt(driverID, 10) + "/icon", "no-cache"},
		{"driver with rev", "/api/drivers/" + strconv.FormatInt(driverID, 10) + "/icon?v=1", "public, max-age=604800"},
		{"driver with malformed rev", "/api/drivers/" + strconv.FormatInt(driverID, 10) + "/icon?v=undefined", "no-cache"},
		{"driver with zero rev", "/api/drivers/" + strconv.FormatInt(driverID, 10) + "/icon?v=0", "no-cache"},
		{"vehicle bare URL", "/api/vehicles/" + strconv.FormatInt(vehicleID, 10) + "/icon", "no-cache"},
		{"vehicle with rev", "/api/vehicles/" + strconv.FormatInt(vehicleID, 10) + "/icon?v=1", "public, max-age=604800"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rec := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want 200; body=%s", tc.path, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != tc.wantCacheCtrl {
				t.Errorf("GET %s: Cache-Control = %q, want %q", tc.path, got, tc.wantCacheCtrl)
			}
			etag := rec.Header().Get("ETag")
			if etag == "" {
				t.Fatalf("GET %s: ETag header missing", tc.path)
			}

			// A conditional re-request (If-None-Match) must return 304 and
			// still carry ETag/Cache-Control — otherwise a client's cached
			// entry never gets its freshness refreshed and falls back to
			// revalidating every subsequent load regardless of the
			// long-cache policy above.
			req2 := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req2.Header.Set("If-None-Match", etag)
			rec2 := httptest.NewRecorder()
			srv.Routes().ServeHTTP(rec2, req2)

			if rec2.Code != http.StatusNotModified {
				t.Fatalf("GET %s (If-None-Match): status = %d, want 304", tc.path, rec2.Code)
			}
			if got := rec2.Header().Get("ETag"); got != etag {
				t.Errorf("GET %s (304): ETag = %q, want %q", tc.path, got, etag)
			}
			if got := rec2.Header().Get("Cache-Control"); got != tc.wantCacheCtrl {
				t.Errorf("GET %s (304): Cache-Control = %q, want %q", tc.path, got, tc.wantCacheCtrl)
			}
		})
	}
}

// TestIconNotFoundIsNoStore covers a 404 (no such id, or no icon yet) never
// getting cached: unlike the icon endpoints' normal exemption from the
// blanket no-store default (see cacheControlExempt), a 404 today can turn
// into a 200 the moment that id gets an icon, so it must not be cached at
// all — including by a shared/edge cache, since these endpoints carry no
// auth.
func TestIconNotFoundIsNoStore(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")

	req := httptest.NewRequest(http.MethodGet, "/api/drivers/"+strconv.FormatInt(driverID, 10)+"/icon", nil)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET icon before any upload: status = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("GET icon 404: Cache-Control = %q, want %q", got, "no-store")
	}
}
