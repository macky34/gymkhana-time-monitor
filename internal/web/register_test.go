package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegisterHappyPath covers POST /api/register's normal path: a driver +
// vehicle + entry are created, the new vehicle becomes the driver's main
// vehicle, and a session cookie is issued.
func TestRegisterHappyPath(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	dtClasses, err := srv.Store.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("ListClassDefs drivetrain: %v", err)
	}

	reqBody := registerRequest{
		Name:          "新規参加者",
		DriverClassID: driverClasses[0].ID,
		Vehicle: vehicleRegInput{
			Number: 61, Name: "登録車両", EngineType: "gasoline",
			DisplacementCC: intp(1000), DrivetrainClassID: dtClasses[0].ID,
		},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(reqBody); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/register", &buf)
	rec := httptest.NewRecorder()
	srv.handleRegister(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var sawSession bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "tm_session" && c.Value != "" {
			sawSession = true
		}
	}
	if !sawSession {
		t.Fatal("no tm_session cookie set on register response")
	}

	out := decodeJSON[struct {
		DriverID int64 `json:"driver_id"`
	}](t, rec.Body.Bytes())

	driver, ok, err := srv.Store.GetDriver(out.DriverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if driver.MainVehicleID == nil {
		t.Fatal("main_vehicle_id not set after register")
	}
	entries, err := srv.Store.ListEntriesByDriver(out.DriverID)
	if err != nil {
		t.Fatalf("ListEntriesByDriver: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "登録車両" {
		t.Fatalf("entries = %+v, want 1 entry named 登録車両", entries)
	}
}

// TestRegisterForbiddenWhenClosed covers POST /api/register once
// registration_open is false on the active event: 403, no driver created.
func TestRegisterForbiddenWhenClosed(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	active, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}
	active.RegistrationOpen = false
	if err := srv.Store.UpdateEvent(active); err != nil {
		t.Fatalf("UpdateEvent: %v", err)
	}

	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	dtClasses, err := srv.Store.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("ListClassDefs drivetrain: %v", err)
	}

	reqBody := registerRequest{
		Name: "拒否される参加者", DriverClassID: driverClasses[0].ID,
		Vehicle: vehicleRegInput{Number: 62, Name: "車両", EngineType: "gasoline", DrivetrainClassID: dtClasses[0].ID},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(reqBody); err != nil {
		t.Fatalf("encode: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/register", &buf)
	rec := httptest.NewRecorder()
	srv.handleRegister(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}
