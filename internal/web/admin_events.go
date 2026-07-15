package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	embedded "timemon"
	"timemon/internal/domain"
	"timemon/internal/store"
)

// adminEventListItem is the shape returned by GET /api/admin/events (and,
// filtered to closed only, GET /api/archive/events).
type adminEventListItem struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Status      string `json:"status"`
	CreatedAtMS int64  `json:"created_at_ms"`
	ClosedAtMS  *int64 `json:"closed_at_ms"`
}

func toAdminEventListItem(e store.EventRow) adminEventListItem {
	return adminEventListItem{
		ID:          e.ID,
		Name:        e.EventName,
		Status:      e.Status,
		CreatedAtMS: e.CreatedAtMS,
		ClosedAtMS:  e.ClosedAtMS,
	}
}

// handleAdminEventsList implements GET /api/admin/events: every event
// (active and closed), newest first.
func (s *Server) handleAdminEventsList(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	events, err := s.Store.ListEvents()
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]adminEventListItem, 0, len(events))
	for _, e := range events {
		out = append(out, toAdminEventListItem(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

type adminEventCreateRequest struct {
	Name         string `json:"name"`
	CopyFromLast bool   `json:"copy_from_last"`
}

// defaultsFile mirrors defaults.json's shape (setup.go's setupRequest posts
// this same "event" sub-shape from the client; here it is read directly on
// the server so a brand new event can fall back to it without a round trip
// through the browser).
type defaultsFile struct {
	Event struct {
		TimingMode       string `json:"timing_mode"`
		PTMode           string `json:"pt_mode"`
		PTPenaltyMS      int    `json:"pt_penalty_ms"`
		HeatRanking      bool   `json:"heat_ranking"`
		RegistrationMode string `json:"registration_mode"`
		QueueSelfEntry   bool   `json:"queue_self_entry"`
		MaxCourseTimeSec int    `json:"max_course_time_sec"`
		SensorLockoutMS  int    `json:"sensor_lockout_ms"`
	} `json:"event"`
	Coefficients        domain.Coefficients `json:"coefficients"`
	DisplacementClasses []domain.DispClass  `json:"displacement_classes"`
}

// loadDefaultEventRow builds an EventRow for a brand new event named "name"
// from the embedded defaults.json (used when there is no prior event to
// copy settings from, or the caller declined to copy).
func loadDefaultEventRow(name string) (store.EventRow, error) {
	var d defaultsFile
	if err := json.Unmarshal(embedded.DefaultsJSON(), &d); err != nil {
		return store.EventRow{}, err
	}
	return store.EventRow{
		EventName:        name,
		TimingMode:       d.Event.TimingMode,
		PTMode:           d.Event.PTMode,
		PTPenaltyMS:      d.Event.PTPenaltyMS,
		HeatRanking:      d.Event.HeatRanking,
		RegistrationMode: d.Event.RegistrationMode,
		RegistrationOpen: true,
		QueueSelfEntry:   d.Event.QueueSelfEntry,
		MaxCourseTimeSec: d.Event.MaxCourseTimeSec,
		SensorLockoutMS:  d.Event.SensorLockoutMS,
		Coef:             d.Coefficients,
		DispClasses:      d.DisplacementClasses,
	}, nil
}

// handleAdminEventCreate implements POST /api/admin/events: creates a new
// event (status='active'). copy_from_last=true reuses the most recently
// created event's configuration columns (name replaced, registration
// reopened); otherwise — or if there is no prior event — defaults.json is
// used. Fails with 409 if an active event already exists
// (store.ErrActiveEventExists, from the events(status) partial unique
// index).
func (s *Server) handleAdminEventCreate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	var req adminEventCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "name is required")
		return
	}

	var set store.EventRow
	copied := false
	if req.CopyFromLast {
		events, err := s.Store.ListEvents()
		if err != nil {
			writeErr(w, err)
			return
		}
		if len(events) > 0 {
			set = events[0] // ListEvents is newest-first; CreateEvent ignores set.ID/Status/CreatedAtMS/ClosedAtMS
			set.EventName = name
			set.RegistrationOpen = true
			copied = true
		}
	}
	if !copied {
		var err error
		set, err = loadDefaultEventRow(name)
		if err != nil {
			writeErr(w, err)
			return
		}
	}

	id, err := s.Store.CreateEvent(set)
	if err != nil {
		if errors.Is(err, store.ErrActiveEventExists) {
			writeJSONError(w, http.StatusConflict, "an active event already exists")
			return
		}
		writeErr(w, err)
		return
	}

	if s.Snap != nil {
		if err := s.Snap.PublishAll(s.Hub); err != nil {
			log.Printf("web: publish all failed: %v", err)
		}
	}
	s.audit(&admin.ID, "admin.event.create", map[string]any{"event_id": id, "name": name, "copy_from_last": req.CopyFromLast})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id})
}

// handleAdminEventClose implements POST /api/admin/events/{id}/close:
// cancels any still-waiting queue rows, then transitions the event to
// 'closed'. Rejected with 409 while any car is on_course (must be finished
// or DNF'd first) or if id is not currently active.
func (s *Server) handleAdminEventClose(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return
	}

	onCourse, err := s.Store.ListQueue(id, "on_course")
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(onCourse) > 0 {
		writeJSONError(w, http.StatusConflict, "cars still on course")
		return
	}

	if err := s.Store.CancelWaiting(id); err != nil {
		writeErr(w, err)
		return
	}

	if err := s.Store.CloseEvent(id); err != nil {
		if errors.Is(err, store.ErrEventNotActive) {
			writeJSONError(w, http.StatusConflict, "event is not active")
			return
		}
		writeErr(w, err)
		return
	}

	if s.Snap != nil {
		if err := s.Snap.PublishAll(s.Hub); err != nil {
			log.Printf("web: publish all failed: %v", err)
		}
	}
	s.audit(&admin.ID, "admin.event.close", map[string]any{"event_id": id})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
