package web

import (
	"encoding/json"
	"net/http"

	"timemon/internal/domain"
	"timemon/internal/store"
)

// handleSetupPage serves GET /setup. Only reachable with the exact token
// generated at process start, and only before the event has been seeded.
// Any other case is a bare 404 (no hint about which condition failed).
func (s *Server) handleSetupPage(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("t")

	s.setupMu.Lock()
	valid := s.setupToken != "" && tok == s.setupToken
	s.setupMu.Unlock()
	if !valid {
		http.NotFound(w, r)
		return
	}

	// Stage-2 (multi-event): gate on "no admin has ever been created" rather
	// than "no active event" — see NewServer's matching comment.
	hasAdmin, err := s.Store.HasAdmin()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if hasAdmin {
		http.NotFound(w, r)
		return
	}

	s.render(w, "setup.html", PageData{SetupMode: true})
}

type setupEventInput struct {
	TimingMode       string `json:"timing_mode"`
	PTMode           string `json:"pt_mode"`
	PTPenaltyMS      int    `json:"pt_penalty_ms"`
	HeatRanking      bool   `json:"heat_ranking"`
	RegistrationMode string `json:"registration_mode"`
	QueueSelfEntry   bool   `json:"queue_self_entry"`
	MaxCourseTimeSec int    `json:"max_course_time_sec"`
	SensorLockoutMS  int    `json:"sensor_lockout_ms"`
}

type setupRequest struct {
	Token               string              `json:"token"`
	EventName           string              `json:"event_name"`
	Event               setupEventInput     `json:"event"`
	Coefficients        domain.Coefficients `json:"coefficients"`
	DisplacementClasses []domain.DispClass  `json:"displacement_classes"`
	Classes             struct {
		Driver     []string `json:"driver"`
		Drivetrain []string `json:"drivetrain"`
	} `json:"classes"`
	Admin struct {
		Name        string `json:"name"`
		DriverClass string `json:"driver_class"`
	} `json:"admin"`
}

func containsStr(list []string, target string) bool {
	for _, v := range list {
		if v == target {
			return true
		}
	}
	return false
}

// handleAPISetup implements POST /api/setup: the one-time event
// configuration wizard. Token mismatch or an already-seeded event both 404
// (the setup token is single-use and cleared on success).
func (s *Server) handleAPISetup(w http.ResponseWriter, r *http.Request) {
	var req setupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}

	s.setupMu.Lock()
	validToken := s.setupToken != "" && req.Token == s.setupToken
	s.setupMu.Unlock()
	if !validToken {
		http.NotFound(w, r)
		return
	}

	hasAdmin, err := s.Store.HasAdmin()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if hasAdmin {
		http.NotFound(w, r)
		return
	}

	// Validate the admin's driver class against the classes we are about to
	// seed *before* writing anything, so a bad request can't leave settings
	// seeded without a usable admin account.
	if !containsStr(req.Classes.Driver, req.Admin.DriverClass) {
		writeJSONError(w, http.StatusBadRequest, "invalid driver_class")
		return
	}

	set := store.EventRow{
		EventName:        req.EventName,
		TimingMode:       req.Event.TimingMode,
		PTMode:           req.Event.PTMode,
		PTPenaltyMS:      req.Event.PTPenaltyMS,
		HeatRanking:      req.Event.HeatRanking,
		RegistrationMode: req.Event.RegistrationMode,
		// RegistrationOpen has no corresponding field in the setup request
		// body; defaulting to open immediately after setup. See report.
		RegistrationOpen: true,
		QueueSelfEntry:   req.Event.QueueSelfEntry,
		MaxCourseTimeSec: req.Event.MaxCourseTimeSec,
		SensorLockoutMS:  req.Event.SensorLockoutMS,
		Coef:             req.Coefficients,
		DispClasses:      req.DisplacementClasses,
	}

	if err := s.Store.SeedEvent(set, req.Classes.Driver, req.Classes.Drivetrain); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	classes, err := s.Store.ListClassDefs("driver")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var classID int64 = -1
	for _, c := range classes {
		if c.Label == req.Admin.DriverClass {
			classID = c.ID
			break
		}
	}
	if classID == -1 {
		// Defensive: should not happen given the pre-check above.
		writeJSONError(w, http.StatusInternalServerError, "driver_class not found after seeding")
		return
	}

	tok, err := randToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	driverID, err := s.Store.CreateDriver(req.Admin.Name, classID, tok, "admin")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.setSessionCookie(w, r, tok)

	s.setupMu.Lock()
	s.setupToken = ""
	s.setupMu.Unlock()

	s.audit(&driverID, "setup", map[string]any{"event_name": req.EventName})
	if s.Snap != nil {
		_ = s.Snap.PublishSettings(s.Hub)
		_ = s.Snap.PublishDirectory(s.Hub)
	}

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
