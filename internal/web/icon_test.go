package web

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"testing"

	"timemon/internal/store"
)

// testIconB64 builds a small, validly-encoded JPEG (decodable by
// image/jpeg, same as iconFromB64 requires) and returns it base64-encoded,
// ready to drop into a {"icon_b64": ...} request body.
func testIconB64(t *testing.T) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("jpeg.Encode: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// TestAdminUserIconRoundTrip covers POST /api/admin/users/{id}/icon plus GET
// /api/drivers/{id}/icon: unset -> 404, set -> 200+ETag, and a matching
// If-None-Match -> 304.
func TestAdminUserIconRoundTrip(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	target, err := srv.Store.CreateDriver("アイコン対象", driverClasses[0].ID, "tok-icon-target", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/drivers/"+itoa(target)+"/icon", nil)
	getReq.SetPathValue("id", itoa(target))
	getRec := httptest.NewRecorder()
	srv.handleDriverIcon(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("GET icon before set: status = %d, want 404", getRec.Code)
	}

	rec := callAdminUsersByID(t, srv.handleAdminUserIcon, http.MethodPost, "/api/admin/users/"+itoa(target)+"/icon",
		target, map[string]any{"icon_b64": testIconB64(t)}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST icon: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	getReq = httptest.NewRequest(http.MethodGet, "/api/drivers/"+itoa(target)+"/icon", nil)
	getReq.SetPathValue("id", itoa(target))
	getRec = httptest.NewRecorder()
	srv.handleDriverIcon(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET icon after set: status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	etag := getRec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag header missing after set")
	}

	condReq := httptest.NewRequest(http.MethodGet, "/api/drivers/"+itoa(target)+"/icon", nil)
	condReq.SetPathValue("id", itoa(target))
	condReq.Header.Set("If-None-Match", etag)
	condRec := httptest.NewRecorder()
	srv.handleDriverIcon(condRec, condReq)
	if condRec.Code != http.StatusNotModified {
		t.Fatalf("GET icon with If-None-Match: status = %d, want 304", condRec.Code)
	}
}

// TestAdminVehicleIconRoundTrip is the vehicle-icon analogue of
// TestAdminUserIconRoundTrip: POST /api/admin/vehicles/{id}/icon plus GET
// /api/vehicles/{id}/icon.
func TestAdminVehicleIconRoundTrip(t *testing.T) {
	srv, _, driverID, vehicleID := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/vehicles/"+itoa(vehicleID)+"/icon", nil)
	getReq.SetPathValue("id", itoa(vehicleID))
	getRec := httptest.NewRecorder()
	srv.handleVehicleIcon(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("GET icon before set: status = %d, want 404", getRec.Code)
	}

	rec := callAdminUsersByID(t, srv.handleAdminVehicleIcon, http.MethodPost, "/api/admin/vehicles/"+itoa(vehicleID)+"/icon",
		vehicleID, map[string]any{"icon_b64": testIconB64(t)}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST icon: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	getReq = httptest.NewRequest(http.MethodGet, "/api/vehicles/"+itoa(vehicleID)+"/icon", nil)
	getReq.SetPathValue("id", itoa(vehicleID))
	getRec = httptest.NewRecorder()
	srv.handleVehicleIcon(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET icon after set: status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	etag := getRec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("ETag header missing after set")
	}

	condReq := httptest.NewRequest(http.MethodGet, "/api/vehicles/"+itoa(vehicleID)+"/icon", nil)
	condReq.SetPathValue("id", itoa(vehicleID))
	condReq.Header.Set("If-None-Match", etag)
	condRec := httptest.NewRecorder()
	srv.handleVehicleIcon(condRec, condReq)
	if condRec.Code != http.StatusNotModified {
		t.Fatalf("GET icon with If-None-Match: status = %d, want 304", condRec.Code)
	}
}

// TestMyVehicleIconUnlinkedVehicleIs404 covers POST
// /api/mypage/vehicles/{id}/icon: a vehicle id the caller has no entries
// link to is reported as a bare 404, same treatment as an unknown id.
func TestMyVehicleIconUnlinkedVehicleIs404(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	driver, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	dtClasses, err := srv.Store.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("ListClassDefs drivetrain: %v", err)
	}
	otherVehicleID, err := srv.Store.CreateVehicle(store.Vehicle{
		Number: 77, Name: "他人の車両", Engine: "gasoline",
		DisplacementCC: intp(1000), DrivetrainClassID: dtClasses[0].ID,
	})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}

	rec := callAdminUsersByID(t, srv.handleMyVehicleIcon, http.MethodPost, "/api/mypage/vehicles/"+itoa(otherVehicleID)+"/icon",
		otherVehicleID, map[string]any{"icon_b64": testIconB64(t)}, driver)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}
