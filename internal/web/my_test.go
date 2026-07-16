package web

import (
	"net/http"
	"testing"

	"timemon/internal/store"
)

// TestMypageVehicleFlow drives a single participant through the mypage
// vehicle self-service surface: (a) a first added vehicle becomes the main
// vehicle automatically, (b) a second addition leaves main unchanged, (c) a
// spec-only edit via PUT ignores the submitted vehicle number (owned by
// event staff) while applying the rest, and (d) deleting the entry for the
// still-main vehicle is rejected with 409.
func TestMypageVehicleFlow(t *testing.T) {
	srv, _, _, _ := newTestServer(t, "sensor")
	driverClasses, err := srv.Store.ListClassDefs("driver")
	if err != nil || len(driverClasses) == 0 {
		t.Fatalf("ListClassDefs driver: %v", err)
	}
	dtClasses, err := srv.Store.ListClassDefs("drivetrain")
	if err != nil || len(dtClasses) == 0 {
		t.Fatalf("ListClassDefs drivetrain: %v", err)
	}

	userID, err := srv.Store.CreateDriver("参加者", driverClasses[0].ID, "tok-participant", "user")
	if err != nil {
		t.Fatalf("CreateDriver: %v", err)
	}
	user, ok, err := srv.Store.GetDriver(userID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if user.MainVehicleID != nil {
		t.Fatalf("fresh driver already has a main vehicle: %v", *user.MainVehicleID)
	}

	// (a) first vehicle becomes main automatically.
	rec := callAdminEvents(t, srv.handleAddMyVehicle, http.MethodPost, "/api/mypage/vehicles", map[string]any{
		"number": 51, "name": "1台目", "engine_type": "gasoline",
		"displacement_cc": 1000, "drivetrain_class_id": dtClasses[0].ID,
	}, user)
	if rec.Code != http.StatusOK {
		t.Fatalf("add 1st vehicle: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	firstVehicle := decodeJSON[struct {
		VehicleID int64 `json:"vehicle_id"`
	}](t, rec.Body.Bytes()).VehicleID

	user, ok, err = srv.Store.GetDriver(userID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if user.MainVehicleID == nil || *user.MainVehicleID != firstVehicle {
		t.Fatalf("main_vehicle_id = %v, want %d after first vehicle add", user.MainVehicleID, firstVehicle)
	}

	// (b) second vehicle does not change main.
	rec = callAdminEvents(t, srv.handleAddMyVehicle, http.MethodPost, "/api/mypage/vehicles", map[string]any{
		"number": 52, "name": "2台目", "engine_type": "gasoline",
		"displacement_cc": 1500, "drivetrain_class_id": dtClasses[0].ID,
	}, user)
	if rec.Code != http.StatusOK {
		t.Fatalf("add 2nd vehicle: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	user, ok, err = srv.Store.GetDriver(userID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	if user.MainVehicleID == nil || *user.MainVehicleID != firstVehicle {
		t.Fatalf("main_vehicle_id = %v, want unchanged %d after second vehicle add", user.MainVehicleID, firstVehicle)
	}

	// (c) PUT ignores the submitted number, applies the rest of the spec.
	rec = callAdminUsersByID(t, srv.handleUpdateMyVehicle, http.MethodPut, "/api/mypage/vehicles/"+itoa(firstVehicle),
		firstVehicle, map[string]any{
			"number": 999, "name": "1台目改", "engine_type": "gasoline",
			"displacement_cc": 1300, "drivetrain_class_id": dtClasses[0].ID,
		}, user)
	if rec.Code != http.StatusOK {
		t.Fatalf("update vehicle: status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	updatedVehicle, ok, err := srv.Store.GetVehicle(firstVehicle)
	if err != nil || !ok {
		t.Fatalf("GetVehicle: ok=%v err=%v", ok, err)
	}
	if updatedVehicle.Number != 51 {
		t.Errorf("number = %d, want unchanged 51 (client-sent 999 must be ignored)", updatedVehicle.Number)
	}
	if updatedVehicle.Name != "1台目改" {
		t.Errorf("name = %q, want %q", updatedVehicle.Name, "1台目改")
	}

	// (d) deleting the entry for the still-main vehicle is rejected with 409.
	rec = callAdminUsersByID(t, srv.handleDeleteMyVehicle, http.MethodDelete, "/api/mypage/vehicles/"+itoa(firstVehicle),
		firstVehicle, nil, user)
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete main vehicle: status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}

	// (e) a vehicle the caller is not linked to is a bare 404 on delete, same
	// as the icon/spec-edit handlers (myVehicleID is the shared ownership
	// check for all three) — not a silent no-op 200.
	otherVehicleID, err := srv.Store.CreateVehicle(store.Vehicle{
		Number: 60, Name: "他人の車両", Engine: "gasoline",
		DisplacementCC: intp(1000), DrivetrainClassID: dtClasses[0].ID,
	})
	if err != nil {
		t.Fatalf("CreateVehicle: %v", err)
	}
	rec = callAdminUsersByID(t, srv.handleDeleteMyVehicle, http.MethodDelete, "/api/mypage/vehicles/"+itoa(otherVehicleID),
		otherVehicleID, nil, user)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete unlinked vehicle: status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok, err := srv.Store.GetVehicle(otherVehicleID); err != nil || !ok {
		t.Fatalf("unlinked vehicle should still exist untouched: ok=%v err=%v", ok, err)
	}
}
