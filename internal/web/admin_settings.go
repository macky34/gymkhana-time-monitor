package web

import (
	"encoding/json"
	"log"
	"net/http"

	"timemon/internal/domain"
	"timemon/internal/store"
)

// adminPublishSettings is the settings-topic analogue of the publishXxx
// helpers in course.go: best-effort, logged not surfaced (the DB mutation
// already succeeded by the time this is called).
func (s *Server) adminPublishSettings() {
	if err := s.Snap.PublishSettings(s.Hub); err != nil {
		log.Printf("web: publish settings failed: %v", err)
	}
}

type adminCoefficientsIO struct {
	TurboGasoline float64 `json:"turbo_gasoline"`
	TurboDiesel   float64 `json:"turbo_diesel"`
	Rotary        float64 `json:"rotary"`
	Supercharger  float64 `json:"supercharger"`
}

type adminDispClassIO struct {
	Label string `json:"label"`
	MaxCC *int   `json:"max_cc"`
}

// adminSettingsIO is both the GET response shape and the expected PUT body
// shape for /api/admin/settings (the spec documents them as identical).
type adminSettingsIO struct {
	EventName           string              `json:"event_name"`
	TimingMode          string              `json:"timing_mode"`
	PTMode              string              `json:"pt_mode"`
	PTPenaltyMS         int                 `json:"pt_penalty_ms"`
	HeatRanking         bool                `json:"heat_ranking"`
	RegistrationMode    string              `json:"registration_mode"`
	RegistrationOpen    bool                `json:"registration_open"`
	QueueSelfEntry      bool                `json:"queue_self_entry"`
	MaxCourseTimeSec    int                 `json:"max_course_time_sec"`
	SensorLockoutMS     int                 `json:"sensor_lockout_ms"`
	Coefficients        adminCoefficientsIO `json:"coefficients"`
	DisplacementClasses []adminDispClassIO  `json:"displacement_classes"`
}

func adminSettingsToIO(set store.SettingsRow) adminSettingsIO {
	dispOut := make([]adminDispClassIO, 0, len(set.DispClasses))
	for _, d := range set.DispClasses {
		dispOut = append(dispOut, adminDispClassIO{Label: d.Label, MaxCC: d.MaxCC})
	}
	return adminSettingsIO{
		EventName:        set.EventName,
		TimingMode:       set.TimingMode,
		PTMode:           set.PTMode,
		PTPenaltyMS:      set.PTPenaltyMS,
		HeatRanking:      set.HeatRanking,
		RegistrationMode: set.RegistrationMode,
		RegistrationOpen: set.RegistrationOpen,
		QueueSelfEntry:   set.QueueSelfEntry,
		MaxCourseTimeSec: set.MaxCourseTimeSec,
		SensorLockoutMS:  set.SensorLockoutMS,
		Coefficients: adminCoefficientsIO{
			TurboGasoline: set.Coef.TurboGasoline,
			TurboDiesel:   set.Coef.TurboDiesel,
			Rotary:        set.Coef.Rotary,
			Supercharger:  set.Coef.Supercharger,
		},
		DisplacementClasses: dispOut,
	}
}

// applyTo overwrites every settings field on set with the values from io
// (a full replace, matching the "同形body" contract for PUT).
func (io adminSettingsIO) applyTo(set store.SettingsRow) store.SettingsRow {
	set.EventName = io.EventName
	set.TimingMode = io.TimingMode
	set.PTMode = io.PTMode
	set.PTPenaltyMS = io.PTPenaltyMS
	set.HeatRanking = io.HeatRanking
	set.RegistrationMode = io.RegistrationMode
	set.RegistrationOpen = io.RegistrationOpen
	set.QueueSelfEntry = io.QueueSelfEntry
	set.MaxCourseTimeSec = io.MaxCourseTimeSec
	set.SensorLockoutMS = io.SensorLockoutMS
	set.Coef = domain.Coefficients{
		TurboGasoline: io.Coefficients.TurboGasoline,
		TurboDiesel:   io.Coefficients.TurboDiesel,
		Rotary:        io.Coefficients.Rotary,
		Supercharger:  io.Coefficients.Supercharger,
	}
	dispClasses := make([]domain.DispClass, 0, len(io.DisplacementClasses))
	for _, d := range io.DisplacementClasses {
		dispClasses = append(dispClasses, domain.DispClass{Label: d.Label, MaxCC: d.MaxCC})
	}
	set.DispClasses = dispClasses
	return set
}

// handleAdminSettingsGet implements GET /api/admin/settings.
func (s *Server) handleAdminSettingsGet(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	set, ok, err := s.Store.GetSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "event not configured")
		return
	}
	writeJSON(w, http.StatusOK, adminSettingsToIO(set))
}

// handleAdminSettingsUpdate implements PUT /api/admin/settings: coefficient
// and PT-mode changes affect converted cc / classes / ranking, so both the
// settings and ranking snapshots are republished.
func (s *Server) handleAdminSettingsUpdate(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	current, ok, err := s.Store.GetSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "event not configured")
		return
	}

	var body adminSettingsIO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}

	updated := body.applyTo(current)
	if err := s.Store.UpdateSettings(updated); err != nil {
		writeErr(w, err)
		return
	}

	s.adminPublishSettings()
	s.publishRanking()

	s.audit(&admin.ID, "admin.settings", map[string]any{"event_name": updated.EventName})

	writeJSON(w, http.StatusOK, adminSettingsToIO(updated))
}

type adminRegistrationBody struct {
	Open bool `json:"open"`
}

// handleAdminRegistrationSet implements PUT /api/admin/registration.
func (s *Server) handleAdminRegistrationSet(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	var body adminRegistrationBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}

	current, ok, err := s.Store.GetSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "event not configured")
		return
	}
	current.RegistrationOpen = body.Open

	if err := s.Store.UpdateSettings(current); err != nil {
		writeErr(w, err)
		return
	}

	s.adminPublishSettings()

	s.audit(&admin.ID, "admin.registration", map[string]any{"open": body.Open})

	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
