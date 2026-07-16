package web

import (
	"net/http"

	"timemon/internal/domain"
	"timemon/internal/store"
)

type adminVehicleBody struct {
	Number            int               `json:"number"`
	Name              string            `json:"name"`
	EngineType        domain.EngineType `json:"engine_type"`
	DisplacementCC    *int              `json:"displacement_cc"`
	ForcedInduction   bool              `json:"forced_induction"`
	DrivetrainClassID int64             `json:"drivetrain_class_id"`
}

// toVehicle converts the wire body into a store.Vehicle for the given id (0
// for a not-yet-created vehicle). EVs never carry a displacement or a
// forced-induction flag, regardless of what the client sent.
func (b adminVehicleBody) toVehicle(id int64) store.Vehicle {
	disp := b.DisplacementCC
	forced := b.ForcedInduction
	if b.EngineType == "ev" {
		disp = nil
		forced = false
	}
	return store.Vehicle{
		ID:                id,
		Number:            b.Number,
		Name:              b.Name,
		Engine:            b.EngineType,
		DisplacementCC:    disp,
		ForcedInduction:   forced,
		DrivetrainClassID: b.DrivetrainClassID,
	}
}

// handleAdminVehicleCreate implements POST /api/admin/vehicles. Adding a
// vehicle can change which cars share a derived displacement class, so the
// ranking snapshot is republished.
func (s *Server) handleAdminVehicleCreate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	body, ok := decodeReqJSON[adminVehicleBody](w, r)
	if !ok {
		return
	}

	id, err := s.Store.CreateVehicle(body.toVehicle(0))
	if err != nil {
		writeErr(w, err)
		return
	}

	warning, err := s.Store.NumberInUse(body.Number, id)
	if err != nil {
		writeErr(w, err)
		return
	}

	s.publishRanking()
	s.publishDirectory()
	s.audit(&admin.ID, "admin.vehicle.create", map[string]any{
		"vehicle_id": id,
		"number":     body.Number,
		"name":       body.Name,
	})

	writeJSON(w, http.StatusOK, map[string]any{"vehicle_id": id, "number_warning": warning})
}

// handleAdminVehicleUpdate implements PUT /api/admin/vehicles/{id}: spec
// changes ripple into converted cc / displacement class / ranking, so the
// ranking snapshot is republished.
func (s *Server) handleAdminVehicleUpdate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	body, ok := decodeReqJSON[adminVehicleBody](w, r)
	if !ok {
		return
	}

	if err := s.Store.UpdateVehicle(body.toVehicle(id)); err != nil {
		writeErr(w, err)
		return
	}

	s.publishRanking()
	s.publishDirectory()
	s.audit(&admin.ID, "admin.vehicle.update", map[string]any{
		"vehicle_id": id,
		"number":     body.Number,
		"name":       body.Name,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminVehicleDelete implements DELETE /api/admin/vehicles/{id}
// (logical delete via store.DeleteVehicle).
func (s *Server) handleAdminVehicleDelete(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}

	if err := s.Store.DeleteVehicle(id); err != nil {
		writeErr(w, err)
		return
	}

	s.publishRanking()
	s.publishDirectory()
	s.audit(&admin.ID, "admin.vehicle.delete", map[string]any{"vehicle_id": id})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminVehicleIcon implements POST /api/admin/vehicles/{id}/icon: sets
// any vehicle's icon, symmetric to handleAdminUserIcon.
func (s *Server) handleAdminVehicleIcon(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	s.applyIcon(w, r, id, s.Store.SetVehicleIcon, func() {
		s.audit(&admin.ID, "admin.vehicle.icon", map[string]any{"vehicle_id": id})
	})
}
