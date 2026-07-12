package web

import (
	"encoding/json"
	"fmt"
	"net/http"

	"timemon/internal/domain"
	"timemon/internal/store"
)

// vehicleRegInput is the shape of the "vehicle" object in both
// POST /api/register and POST /api/my/vehicles: either {"vehicle_id":N} to
// join an existing car, or full details to create a new one.
type vehicleRegInput struct {
	VehicleID         *int64 `json:"vehicle_id,omitempty"`
	Number            int    `json:"number,omitempty"`
	Name              string `json:"name,omitempty"`
	EngineType        string `json:"engine_type,omitempty"`
	DisplacementCC    *int   `json:"displacement_cc,omitempty"`
	ForcedInduction   bool   `json:"forced_induction,omitempty"`
	DrivetrainClassID int64  `json:"drivetrain_class_id,omitempty"`
}

type registerRequest struct {
	Name          string          `json:"name"`
	DriverClassID int64           `json:"driver_class_id"`
	IconB64       *string         `json:"icon_b64"`
	Vehicle       vehicleRegInput `json:"vehicle"`
}

// resolveOrCreateVehicle either resolves an existing vehicle_id or creates a
// new vehicle. numberWarning is true when a newly created vehicle's number
// collides with an existing vehicle (advisory only - registration is not
// blocked on it).
func (s *Server) resolveOrCreateVehicle(in vehicleRegInput) (vehicleID int64, numberWarning bool, err error) {
	if in.VehicleID != nil {
		v, ok, gerr := s.Store.GetVehicle(*in.VehicleID)
		if gerr != nil {
			return 0, false, gerr
		}
		if !ok {
			return 0, false, fmt.Errorf("vehicle not found")
		}
		return v.ID, false, nil
	}

	inUse, uerr := s.Store.NumberInUse(in.Number, 0)
	if uerr != nil {
		return 0, false, uerr
	}
	id, cerr := s.Store.CreateVehicle(store.Vehicle{
		Number:            in.Number,
		Name:              in.Name,
		Engine:            domain.EngineType(in.EngineType),
		DisplacementCC:    in.DisplacementCC,
		ForcedInduction:   in.ForcedInduction,
		DrivetrainClassID: in.DrivetrainClassID,
	})
	if cerr != nil {
		return 0, false, cerr
	}
	return id, inUse, nil
}

// handleRegister implements POST /api/register: public/queue self-service
// sign-up. Gated by settings.RegistrationMode/RegistrationOpen.
func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	set, ok, err := s.Store.GetSettings()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok || set.RegistrationMode == "staff" || !set.RegistrationOpen {
		writeJSONError(w, http.StatusForbidden, "registration is closed")
		return
	}

	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}

	// Validate/decode the icon before writing anything, so a bad image
	// can't leave a half-registered driver behind.
	var iconJPEG []byte
	if req.IconB64 != nil && *req.IconB64 != "" {
		iconJPEG, err = iconFromB64(*req.IconB64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid icon")
			return
		}
	}

	tok, err := randToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	driverID, err := s.Store.CreateDriver(req.Name, req.DriverClassID, tok, "user")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	vehicleID, numberWarning, err := s.resolveOrCreateVehicle(req.Vehicle)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.AddEntry(driverID, vehicleID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Store.SetMainVehicle(driverID, vehicleID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if iconJPEG != nil {
		if err := s.Store.SetIcon(driverID, iconJPEG); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	s.setSessionCookie(w, r, tok)
	s.audit(&driverID, "register", map[string]any{"vehicle_id": vehicleID})

	resp := map[string]any{"driver_id": driverID}
	if numberWarning {
		resp["number_warning"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}
