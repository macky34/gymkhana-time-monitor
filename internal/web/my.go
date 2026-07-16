package web

import (
	"encoding/json"
	"net/http"
	"sort"

	qrcode "github.com/skip2/go-qrcode"

	"timemon/internal/domain"
	"timemon/internal/store"
)

// queueStateFor answers "where is this driver in the queue/course right
// now", for the "queue" field of GET /api/mypage. With no active event,
// there is nowhere the driver could be queued, so this reports "none"
// without touching the store.
func (s *Server) queueStateFor(driverID int64) map[string]any {
	ev, ok, err := s.Store.GetActiveEvent()
	if err != nil || !ok {
		return map[string]any{"state": "none"}
	}

	waiting, err := s.Store.ListQueue(ev.ID, "waiting")
	if err == nil {
		for i, q := range waiting {
			if q.DriverID == driverID {
				return map[string]any{"state": "waiting", "position": i + 1, "queue_id": q.ID}
			}
		}
	}

	onCourse, err := s.Store.ListQueue(ev.ID, "on_course")
	if err == nil {
		for _, q := range onCourse {
			if q.DriverID == driverID {
				state := "running"
				if q.TStartUS == nil {
					state = "ready"
				}
				return map[string]any{
					"state":      state,
					"queue_id":   q.ID,
					"t_start_us": q.TStartUS,
					"pt_count":   q.PTCount,
					"mc_flag":    q.MCFlag,
				}
			}
		}
	}

	return map[string]any{"state": "none"}
}

type runOut struct {
	VehicleID   int64  `json:"vehicle_id"`
	Heat        int    `json:"heat"`
	RawMS       int    `json:"raw_ms"`
	PTCount     int    `json:"pt_count"`
	IsMC        bool   `json:"is_mc"`
	FinalMS     int    `json:"final_ms"`
	Invalid     bool   `json:"invalid"`
	TimestampMS int64  `json:"timestamp_ms"`
	Source      string `json:"source"`
}

// buildRunsFor projects every run belonging to driverID in the active event
// into the "runs" array of GET /api/mypage, newest first. Heat numbers are
// computed over *all* runs (domain.HeatNumbers needs full context) then
// filtered down. With no active event, this is an empty list.
func (s *Server) buildRunsFor(driverID int64) ([]runOut, error) {
	set, ok, err := s.Store.GetActiveEvent()
	if err != nil {
		return nil, err
	}
	if !ok {
		return []runOut{}, nil
	}

	allRuns, err := s.Store.ListRuns(set.ID)
	if err != nil {
		return nil, err
	}
	heatNums := domain.HeatNumbers(allRuns)

	out := make([]runOut, 0)
	for _, run := range allRuns {
		if run.Combo.DriverID != driverID {
			continue
		}
		final, invalid := domain.FinalMS(run.RawMS, run.PTCount, run.IsMC, set.PTMode, set.PTPenaltyMS)

		source := ""
		if lg, ok, lerr := s.Store.GetLog(run.LogID); lerr == nil && ok {
			source = lg.Source
		}

		out = append(out, runOut{
			VehicleID:   run.Combo.VehicleID,
			Heat:        heatNums[run.LogID],
			RawMS:       run.RawMS,
			PTCount:     run.PTCount,
			IsMC:        run.IsMC,
			FinalMS:     final,
			Invalid:     invalid,
			TimestampMS: run.TimestampMS,
			Source:      source,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].TimestampMS > out[j].TimestampMS })
	return out, nil
}

func (s *Server) handleGetMy(w http.ResponseWriter, r *http.Request, d store.Driver) {
	type driverOut struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		DriverClassID int64  `json:"driver_class_id"`
		Role          string `json:"role"`
		HasIcon       bool   `json:"has_icon"`
	}

	coef, dispClasses, dtLabel, err := s.loadVehicleContext()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	entries, err := s.Store.ListEntriesByDriver(d.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	vehiclesOut := make([]vehicleOut, 0, len(entries))
	for _, v := range entries {
		vo, verr := s.buildVehicleOut(v, coef, dispClasses, dtLabel)
		if verr != nil {
			writeJSONError(w, http.StatusInternalServerError, verr.Error())
			return
		}
		vehiclesOut = append(vehiclesOut, vo)
	}

	runs, err := s.buildRunsFor(d.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var mainVehicleID any
	if d.MainVehicleID != nil {
		mainVehicleID = *d.MainVehicleID
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"driver": driverOut{
			ID:            d.ID,
			Name:          d.Name,
			DriverClassID: d.DriverClassID,
			Role:          d.Role,
			HasIcon:       d.HasIcon,
		},
		"main_vehicle_id": mainVehicleID,
		"vehicles":        vehiclesOut,
		"queue":           s.queueStateFor(d.ID),
		"runs":            runs,
		"login_url":       s.BaseURL + "/a/" + d.Token,
	})
}

type updateProfileBody struct {
	Name          string `json:"name"`
	DriverClassID int64  `json:"driver_class_id"`
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request, d store.Driver) {
	body, ok := decodeReqJSON[updateProfileBody](w, r)
	if !ok {
		return
	}
	if err := s.Store.UpdateDriver(d.ID, body.Name, body.DriverClassID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishDirectory()
	s.audit(&d.ID, "my.profile", map[string]any{"name": body.Name, "driver_class_id": body.DriverClassID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMyIcon(w http.ResponseWriter, r *http.Request, d store.Driver) {
	s.applyIcon(w, r, d.ID, s.Store.SetIcon, func() {
		s.audit(&d.ID, "my.icon", map[string]any{})
	})
}

// handleMyVehicleIcon implements POST /api/mypage/vehicles/{id}/icon: sets the
// icon of a vehicle the caller is linked to via entries. Vehicles the
// caller is not linked to are reported as a bare 404, same as an unknown
// vehicle id (no existence leakage), mirroring handleDeleteMyVehicle's
// treatment of vehicle ids that are not the caller's.
func (s *Server) handleMyVehicleIcon(w http.ResponseWriter, r *http.Request, d store.Driver) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	entries, err := s.Store.ListEntriesByDriver(d.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	found := false
	for _, v := range entries {
		if v.ID == id {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	s.applyIcon(w, r, id, s.Store.SetVehicleIcon, func() {
		s.audit(&d.ID, "my.vehicle.icon", map[string]any{"vehicle_id": id})
	})
}

// handleUpdateMyVehicle implements PUT /api/mypage/vehicles/{id}: lets the
// caller edit the spec of a vehicle they are linked to via entries (same
// ownership check, and the same 404-for-not-mine treatment, as
// handleMyVehicleIcon). Reuses adminVehicleBody (admin_vehicles.go) for the
// wire shape/EV-nulling so the field names and validation match the admin
// vehicle-update handler exactly. Vehicle number/name feed the ranking,
// queue and on-course displays, so publishAll+publishDirectory mirror
// handleMyVehicleIcon (rather than handleAdminVehicleUpdate's
// publishRanking-only, since that handler has no icon-touching path).
func (s *Server) handleUpdateMyVehicle(w http.ResponseWriter, r *http.Request, d store.Driver) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}

	entries, err := s.Store.ListEntriesByDriver(d.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	found := false
	for _, v := range entries {
		if v.ID == id {
			found = true
			break
		}
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	current, ok, err := s.Store.GetVehicle(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}

	body, ok := decodeReqJSON[adminVehicleBody](w, r)
	if !ok {
		return
	}
	// The 号車番号 is assigned/managed by event staff, not the driver — a
	// participant editing their own vehicle from mypage may change the
	// spec fields but never the number, regardless of what the request
	// body contains.
	body.Number = current.Number

	if err := s.Store.UpdateVehicle(body.toVehicle(id)); err != nil {
		writeErr(w, err)
		return
	}

	s.publishAll()
	s.publishDirectory()
	s.audit(&d.ID, "my.vehicle.update", map[string]any{
		"vehicle_id": id,
		"name":       body.Name,
	})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMyQR(w http.ResponseWriter, r *http.Request, d store.Driver) {
	png, err := qrcode.Encode(s.BaseURL+"/a/"+d.Token, qrcode.Medium, 256)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

func (s *Server) handleAddMyVehicle(w http.ResponseWriter, r *http.Request, d store.Driver) {
	body, ok := decodeReqJSON[vehicleRegInput](w, r)
	if !ok {
		return
	}
	vehicleID, numberWarning, err := s.resolveOrCreateVehicle(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Store.AddEntry(d.ID, vehicleID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishDirectory()
	s.audit(&d.ID, "my.vehicle.add", map[string]any{"vehicle_id": vehicleID})

	resp := map[string]any{"vehicle_id": vehicleID}
	if numberWarning {
		resp["number_warning"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleDeleteMyVehicle(w http.ResponseWriter, r *http.Request, d store.Driver) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if d.MainVehicleID != nil && *d.MainVehicleID == id {
		writeJSONError(w, http.StatusConflict, "メイン車両は解除できません")
		return
	}
	if err := s.Store.DeleteEntry(d.ID, id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishDirectory()
	s.audit(&d.ID, "my.vehicle.del", map[string]any{"vehicle_id": id})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleSetMainVehicle(w http.ResponseWriter, r *http.Request, d store.Driver) {
	body, ok := decodeReqJSON[struct {
		VehicleID int64 `json:"vehicle_id"`
	}](w, r)
	if !ok {
		return
	}

	entries, err := s.Store.ListEntriesByDriver(d.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	found := false
	for _, v := range entries {
		if v.ID == body.VehicleID {
			found = true
			break
		}
	}
	if !found {
		writeJSONError(w, http.StatusForbidden, "自分の車両ではありません")
		return
	}

	if err := s.Store.SetMainVehicle(d.ID, body.VehicleID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.publishDirectory()
	s.audit(&d.ID, "my.main", map[string]any{"vehicle_id": body.VehicleID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMyQueueAdd(w http.ResponseWriter, r *http.Request, d store.Driver) {
	set, ok := s.requireActiveEvent(w)
	if !ok {
		return
	}
	if !set.QueueSelfEntry {
		writeJSONError(w, http.StatusForbidden, "self entry disabled")
		return
	}

	var body struct {
		VehicleID *int64 `json:"vehicle_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional

	var vehicleID int64
	switch {
	case body.VehicleID != nil:
		vehicleID = *body.VehicleID
	case d.MainVehicleID != nil:
		vehicleID = *d.MainVehicleID
	default:
		writeJSONError(w, http.StatusBadRequest, "no vehicle")
		return
	}

	waiting, err := s.Store.ListQueue(set.ID, "waiting")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, q := range waiting {
		if q.DriverID == d.ID {
			writeJSONError(w, http.StatusConflict, "already queued")
			return
		}
	}
	onCourse, err := s.Store.ListQueue(set.ID, "on_course")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, q := range onCourse {
		if q.DriverID == d.ID {
			writeJSONError(w, http.StatusConflict, "already on course")
			return
		}
	}

	createdBy := d.ID
	id, err := s.Store.Enqueue(set.ID, d.ID, vehicleID, &createdBy)
	if err != nil {
		writeJSONError(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(&d.ID, "my.queue.add", map[string]any{"vehicle_id": vehicleID, "queue_id": id})
	if s.Snap != nil {
		_ = s.Snap.PublishQueue(s.Hub)
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue_id": id})
}

func (s *Server) handleMyQueueCancel(w http.ResponseWriter, r *http.Request, d store.Driver) {
	ev, ok, err := s.Store.GetActiveEvent()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var waiting []store.QueueRow
	if ok {
		waiting, err = s.Store.ListQueue(ev.ID, "waiting")
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	var mine *store.QueueRow
	for i := range waiting {
		if waiting[i].DriverID == d.ID {
			mine = &waiting[i]
			break
		}
	}
	if mine == nil {
		writeJSONError(w, http.StatusNotFound, "not queued")
		return
	}

	if err := s.Store.SetQueueStatus(mine.ID, "canceled"); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(&d.ID, "my.queue.del", map[string]any{"queue_id": mine.ID})
	if s.Snap != nil {
		_ = s.Snap.PublishQueue(s.Hub)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
