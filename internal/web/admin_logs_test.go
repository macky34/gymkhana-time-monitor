package web

import (
	"net/http"
	"testing"
	"time"

	"timemon/internal/store"
)

// TestAdminLogUpdateUnassignDriverVehicle covers PUT /api/admin/logs/{id}
// with driver_id/vehicle_id set to null: this must clear the assignment
// (store a NULL FK), not write id=0 and trip the FOREIGN KEY constraint.
func TestAdminLogUpdateUnassignDriverVehicle(t *testing.T) {
	srv, _, driverID, vehicleID := newTestServer(t, "sensor")
	admin, ok, err := srv.Store.GetDriver(driverID)
	if err != nil || !ok {
		t.Fatalf("GetDriver: ok=%v err=%v", ok, err)
	}
	activeEvent, ok, err := srv.Store.GetActiveEvent()
	if err != nil || !ok {
		t.Fatalf("GetActiveEvent: ok=%v err=%v", ok, err)
	}

	logID, err := srv.Store.InsertLog(store.LogRow{
		EventID:     activeEvent.ID,
		DriverID:    &driverID,
		VehicleID:   &vehicleID,
		RawMS:       12345,
		TimestampMS: time.Now().UnixMilli(),
		Source:      "manual",
	})
	if err != nil {
		t.Fatalf("InsertLog: %v", err)
	}

	rec := callAdminUsersByID(t, srv.handleAdminLogUpdate, http.MethodPut, "/api/admin/logs/"+itoa(logID),
		logID, map[string]any{
			"driver_id": nil, "vehicle_id": nil, "raw_ms": 12345, "pt_count": 0, "is_mc": false,
		}, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	updated, ok, err := srv.Store.GetLog(logID)
	if err != nil || !ok {
		t.Fatalf("GetLog: ok=%v err=%v", ok, err)
	}
	if updated.DriverID != nil || updated.VehicleID != nil {
		t.Errorf("log = %+v, want DriverID=nil VehicleID=nil (unassigned)", updated)
	}
}
