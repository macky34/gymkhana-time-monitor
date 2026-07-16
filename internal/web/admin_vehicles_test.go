package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdminVehicleCreate covers POST /api/admin/vehicles: the created row is
// persisted with the submitted spec.
func TestAdminVehicleCreate(t *testing.T) {
	srv, _, driverID, _ := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	dtClasses, err := srv.Store.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("ListClassDefs drivetrain: %v", err)
	}

	rec := callAdminEvents(t, srv.handleAdminVehicleCreate, http.MethodPost, "/api/admin/vehicles",
		map[string]any{
			"number": 42, "name": "新規車両", "engine_type": "gasoline",
			"displacement_cc": 1000, "forced_induction": false, "drivetrain_class_id": dtClasses[0].ID,
		}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	out := decodeJSON[struct {
		VehicleID int64 `json:"vehicle_id"`
	}](t, rec.Body.Bytes())

	created, ok, err := srv.Store.GetVehicle(out.VehicleID)
	if err != nil || !ok {
		t.Fatalf("GetVehicle: ok=%v err=%v", ok, err)
	}
	if created.Number != 42 || created.Name != "新規車両" {
		t.Errorf("vehicle = %+v, want number=42 name=新規車両", created)
	}
}

// TestAdminVehicleUpdateReflectsInList covers PUT /api/admin/vehicles/{id}:
// a spec change is visible in GET /api/vehicles afterward.
func TestAdminVehicleUpdateReflectsInList(t *testing.T) {
	srv, _, driverID, vehicleID := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	dtClasses, err := srv.Store.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("ListClassDefs drivetrain: %v", err)
	}

	rec := callAdminUsersByID(t, srv.handleAdminVehicleUpdate, http.MethodPut, "/api/admin/vehicles/"+itoa(vehicleID),
		vehicleID, map[string]any{
			"number": 99, "name": "改名車両", "engine_type": "gasoline",
			"displacement_cc": 2000, "forced_induction": true, "drivetrain_class_id": dtClasses[0].ID,
		}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil)
	listRec := httptest.NewRecorder()
	srv.handleAPIVehicles(listRec, req)
	if listRec.Code != http.StatusOK {
		t.Fatalf("GET /api/vehicles: status = %d, want 200; body=%s", listRec.Code, listRec.Body.String())
	}
	list := decodeJSON[struct {
		Vehicles []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Number int    `json:"number"`
		} `json:"vehicles"`
	}](t, listRec.Body.Bytes())

	var found bool
	for _, v := range list.Vehicles {
		if v.ID == vehicleID {
			found = true
			if v.Name != "改名車両" || v.Number != 99 {
				t.Errorf("vehicle in list = %+v, want name=改名車両 number=99", v)
			}
		}
	}
	if !found {
		t.Fatalf("vehicle %d not found in GET /api/vehicles list", vehicleID)
	}
}

// TestAdminVehicleDeleteRemovesFromList covers DELETE
// /api/admin/vehicles/{id}: the logically-deleted vehicle disappears from
// GET /api/vehicles.
func TestAdminVehicleDeleteRemovesFromList(t *testing.T) {
	srv, _, driverID, vehicleID := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}

	rec := callAdminUsersByID(t, srv.handleAdminVehicleDelete, http.MethodDelete, "/api/admin/vehicles/"+itoa(vehicleID),
		vehicleID, nil, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/vehicles", nil)
	listRec := httptest.NewRecorder()
	srv.handleAPIVehicles(listRec, req)
	list := decodeJSON[struct {
		Vehicles []struct {
			ID int64 `json:"id"`
		} `json:"vehicles"`
	}](t, listRec.Body.Bytes())
	for _, v := range list.Vehicles {
		if v.ID == vehicleID {
			t.Fatalf("deleted vehicle %d still present in GET /api/vehicles list", vehicleID)
		}
	}
}
