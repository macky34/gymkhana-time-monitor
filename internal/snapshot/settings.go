package snapshot

import "encoding/json"

type settingsResponse struct {
	EventName        string     `json:"event_name"`
	TimingMode       string     `json:"timing_mode"`
	PTMode           string     `json:"pt_mode"`
	PTPenaltyMS      int        `json:"pt_penalty_ms"`
	HeatRanking      bool       `json:"heat_ranking"`
	RegistrationMode string     `json:"registration_mode"`
	RegistrationOpen bool       `json:"registration_open"`
	QueueSelfEntry   bool       `json:"queue_self_entry"`
	MaxCourseTimeSec int        `json:"max_course_time_sec"`
	DispClasses      []string   `json:"disp_classes"`
	DriverClasses    []classRef `json:"driver_classes"`
	DTClasses        []classRef `json:"dt_classes"`
}

type classRef struct {
	ID    int64  `json:"id"`
	Label string `json:"label"`
}

// Settings builds the event-configuration snapshot. "EV" is appended to
// disp_classes whenever at least one registered vehicle has Engine == "ev",
// regardless of whether that vehicle has logged any runs yet.
func (b *Builder) Settings() ([]byte, error) {
	settings, _, err := b.s.GetSettings()
	if err != nil {
		return nil, err
	}
	driverClasses, err := b.s.ListClassDefs("driver")
	if err != nil {
		return nil, err
	}
	dtClasses, err := b.s.ListClassDefs("drivetrain")
	if err != nil {
		return nil, err
	}
	vehicles, err := b.s.ListVehicles()
	if err != nil {
		return nil, err
	}

	dispClasses := make([]string, 0, len(settings.DispClasses)+1)
	for _, dc := range settings.DispClasses {
		dispClasses = append(dispClasses, dc.Label)
	}
	for _, v := range vehicles {
		if v.Engine == evEngine {
			dispClasses = append(dispClasses, "EV")
			break
		}
	}

	driverClassRefs := make([]classRef, 0, len(driverClasses))
	for _, c := range driverClasses {
		driverClassRefs = append(driverClassRefs, classRef{ID: c.ID, Label: c.Label})
	}
	dtClassRefs := make([]classRef, 0, len(dtClasses))
	for _, c := range dtClasses {
		dtClassRefs = append(dtClassRefs, classRef{ID: c.ID, Label: c.Label})
	}

	return json.Marshal(settingsResponse{
		EventName:        settings.EventName,
		TimingMode:       settings.TimingMode,
		PTMode:           settings.PTMode,
		PTPenaltyMS:      settings.PTPenaltyMS,
		HeatRanking:      settings.HeatRanking,
		RegistrationMode: settings.RegistrationMode,
		RegistrationOpen: settings.RegistrationOpen,
		QueueSelfEntry:   settings.QueueSelfEntry,
		MaxCourseTimeSec: settings.MaxCourseTimeSec,
		DispClasses:      dispClasses,
		DriverClasses:    driverClassRefs,
		DTClasses:        dtClassRefs,
	})
}
