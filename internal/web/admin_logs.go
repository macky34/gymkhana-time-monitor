package web

import (
	"net/http"
	"strconv"
	"time"

	"timemon/internal/domain"
	"timemon/internal/store"
)

type adminLogDriverOut struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	HasIcon bool   `json:"has_icon"`
}

type adminLogVehicleOut struct {
	ID      int64  `json:"id"`
	Number  int    `json:"number"`
	Name    string `json:"name"`
	HasIcon bool   `json:"has_icon"`
}

type adminLogOut struct {
	ID          int64               `json:"id"`
	Driver      *adminLogDriverOut  `json:"driver"`
	Vehicle     *adminLogVehicleOut `json:"vehicle"`
	RawMS       int                 `json:"raw_ms"`
	PTCount     int                 `json:"pt_count"`
	IsMC        bool                `json:"is_mc"`
	FinalMS     int                 `json:"final_ms"`
	Invalid     bool                `json:"invalid"`
	TimestampMS int64               `json:"timestamp_ms"`
	Source      string              `json:"source"`
	EditedAt    *int64              `json:"edited_at"`
	IsDeleted   bool                `json:"is_deleted"`
	Heat        *int                `json:"heat"`
}

// buildAdminLogOut projects a raw store.LogRow into the admin logs JSON
// shape: driver/vehicle resolve to {id,name}/{id,number,name} (nil when
// unassigned or when the referenced row is gone), final_ms/invalid come from
// domain.FinalMS under the current settings, and heat is looked up from a
// HeatNumbers map the caller computed over every run - a miss (unassigned or
// deleted logs are not "runs") surfaces as a null heat.
func (s *Server) buildAdminLogOut(l store.LogRow, set store.EventRow, heatNums map[int64]int) adminLogOut {
	final, invalid := domain.FinalMS(l.RawMS, l.PTCount, l.IsMC, set.PTMode, set.PTPenaltyMS)

	out := adminLogOut{
		ID:          l.ID,
		RawMS:       l.RawMS,
		PTCount:     l.PTCount,
		IsMC:        l.IsMC,
		FinalMS:     final,
		Invalid:     invalid,
		TimestampMS: l.TimestampMS,
		Source:      l.Source,
		EditedAt:    l.EditedAt,
		IsDeleted:   l.IsDeleted,
	}

	if l.DriverID != nil {
		if d, ok, err := s.Store.GetDriver(*l.DriverID); err == nil && ok {
			out.Driver = &adminLogDriverOut{ID: d.ID, Name: d.Name, HasIcon: d.HasIcon}
		}
	}
	if l.VehicleID != nil {
		if v, ok, err := s.Store.GetVehicle(*l.VehicleID); err == nil && ok {
			out.Vehicle = &adminLogVehicleOut{ID: v.ID, Number: v.Number, Name: v.Name, HasIcon: v.HasIcon}
		}
	}
	if h, ok := heatNums[l.ID]; ok {
		hh := h
		out.Heat = &hh
	}

	return out
}

const adminLogsPageSize = 50

// handleAdminLogsList implements GET /api/admin/logs?page=1.
func (s *Server) handleAdminLogsList(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	page := 1
	if p, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && p > 0 {
		page = p
	}
	offset := (page - 1) * adminLogsPageSize

	set, ok, err := s.Store.GetActiveEvent()
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"logs":       []adminLogOut{},
			"total":      0,
			"unassigned": []adminLogOut{},
		})
		return
	}

	logs, total, err := s.Store.ListLogs(set.ID, adminLogsPageSize, offset)
	if err != nil {
		writeErr(w, err)
		return
	}
	unassigned, err := s.Store.ListUnassignedLogs(set.ID)
	if err != nil {
		writeErr(w, err)
		return
	}

	allRuns, err := s.Store.ListRuns(set.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	heatNums := domain.HeatNumbers(allRuns)

	logsOut := make([]adminLogOut, 0, len(logs))
	for _, l := range logs {
		logsOut = append(logsOut, s.buildAdminLogOut(l, set, heatNums))
	}
	unassignedOut := make([]adminLogOut, 0, len(unassigned))
	for _, l := range unassigned {
		unassignedOut = append(unassignedOut, s.buildAdminLogOut(l, set, heatNums))
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"logs":       logsOut,
		"total":      total,
		"unassigned": unassignedOut,
	})
}

type adminLogCreateBody struct {
	DriverID    int64  `json:"driver_id"`
	VehicleID   int64  `json:"vehicle_id"`
	RawMS       int    `json:"raw_ms"`
	PTCount     int    `json:"pt_count"`
	IsMC        bool   `json:"is_mc"`
	TimestampMS *int64 `json:"timestamp_ms"`
}

// handleAdminLogCreate implements POST /api/admin/logs: manual entry of a
// timing log.
func (s *Server) handleAdminLogCreate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	body, ok := decodeReqJSON[adminLogCreateBody](w, r)
	if !ok {
		return
	}

	ev, ok := s.requireActiveEvent(w)
	if !ok {
		return
	}

	ts := time.Now().UnixMilli()
	if body.TimestampMS != nil {
		ts = *body.TimestampMS
	}
	driverID := body.DriverID
	vehicleID := body.VehicleID

	id, err := s.Store.InsertLog(store.LogRow{
		EventID:     ev.ID,
		DriverID:    &driverID,
		VehicleID:   &vehicleID,
		RawMS:       body.RawMS,
		PTCount:     body.PTCount,
		IsMC:        body.IsMC,
		TimestampMS: ts,
		Source:      "manual",
	})
	if err != nil {
		writeErr(w, err)
		return
	}

	s.publishRanking()
	s.audit(&admin.ID, "admin.log.add", map[string]any{
		"log_id":     id,
		"driver_id":  body.DriverID,
		"vehicle_id": body.VehicleID,
		"raw_ms":     body.RawMS,
	})

	writeJSON(w, http.StatusOK, map[string]any{"log_id": id})
}

type adminLogUpdateBody struct {
	DriverID  *int64 `json:"driver_id"`
	VehicleID *int64 `json:"vehicle_id"`
	RawMS     int    `json:"raw_ms"`
	PTCount   int    `json:"pt_count"`
	IsMC      bool   `json:"is_mc"`
}

// handleAdminLogUpdate implements PUT /api/admin/logs/{id}: corrects a
// timing log's raw fields and stamps edited_at.
func (s *Server) handleAdminLogUpdate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	body, ok := decodeReqJSON[adminLogUpdateBody](w, r)
	if !ok {
		return
	}

	l, ok, err := s.Store.GetLog(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	l.DriverID = body.DriverID
	l.VehicleID = body.VehicleID
	l.RawMS = body.RawMS
	l.PTCount = body.PTCount
	l.IsMC = body.IsMC
	now := time.Now().UnixMilli()
	l.EditedAt = &now

	if err := s.Store.UpdateLog(l); err != nil {
		writeErr(w, err)
		return
	}

	s.publishRanking()
	s.audit(&admin.ID, "admin.log.update", map[string]any{"log_id": id})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAdminLogDelete implements DELETE /api/admin/logs/{id} (soft delete).
func (s *Server) handleAdminLogDelete(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}

	if err := s.Store.SoftDeleteLog(id); err != nil {
		writeErr(w, err)
		return
	}

	s.publishRanking()
	s.audit(&admin.ID, "admin.log.delete", map[string]any{"log_id": id})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type adminLogAssignBody struct {
	DriverID  int64 `json:"driver_id"`
	VehicleID int64 `json:"vehicle_id"`
}

// handleAdminLogAssign implements PUT /api/admin/logs/{id}/assign: attaches
// a driver/vehicle to a previously unassigned (or misassigned) log.
func (s *Server) handleAdminLogAssign(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	body, ok := decodeReqJSON[adminLogAssignBody](w, r)
	if !ok {
		return
	}

	l, ok, err := s.Store.GetLog(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	driverID := body.DriverID
	vehicleID := body.VehicleID
	l.DriverID = &driverID
	l.VehicleID = &vehicleID

	if err := s.Store.UpdateLog(l); err != nil {
		writeErr(w, err)
		return
	}

	s.publishRanking()
	s.audit(&admin.ID, "admin.log.assign", map[string]any{
		"log_id":     id,
		"driver_id":  body.DriverID,
		"vehicle_id": body.VehicleID,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
