package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"timemon/internal/domain"
)

// SettingsRow mirrors the settings table's single row. Coef/DispClasses are
// parsed from the coefficients/displacement_classes JSON columns into
// domain types.
type SettingsRow struct {
	EventName        string
	TimingMode       string
	PTMode           string
	PTPenaltyMS      int
	HeatRanking      bool
	RegistrationMode string
	RegistrationOpen bool
	QueueSelfEntry   bool
	MaxCourseTimeSec int
	SensorLockoutMS  int
	Coef             domain.Coefficients
	DispClasses      []domain.DispClass
}

// GetSettings returns the single settings row. ok=false means no settings
// row exists yet (fresh DB) — the caller uses that to decide whether the
// event still needs first-run setup (plan/DESIGN.md §2.1).
func (s *Store) GetSettings() (SettingsRow, bool, error) {
	row := s.db.QueryRow(`SELECT event_name, timing_mode, pt_mode, pt_penalty_ms, heat_ranking,
		registration_mode, registration_open, queue_self_entry, max_course_time_sec, sensor_lockout_ms,
		coefficients, displacement_classes
		FROM settings WHERE id = 1`)

	var out SettingsRow
	var heatRanking, regOpen, selfEntry int
	var coefJSON, dispJSON string
	err := row.Scan(&out.EventName, &out.TimingMode, &out.PTMode, &out.PTPenaltyMS, &heatRanking,
		&out.RegistrationMode, &regOpen, &selfEntry, &out.MaxCourseTimeSec, &out.SensorLockoutMS,
		&coefJSON, &dispJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return SettingsRow{}, false, nil
	}
	if err != nil {
		return SettingsRow{}, false, fmt.Errorf("store: get settings: %w", err)
	}

	out.HeatRanking = heatRanking != 0
	out.RegistrationOpen = regOpen != 0
	out.QueueSelfEntry = selfEntry != 0

	if err := json.Unmarshal([]byte(coefJSON), &out.Coef); err != nil {
		return SettingsRow{}, false, fmt.Errorf("store: parse coefficients: %w", err)
	}
	if err := json.Unmarshal([]byte(dispJSON), &out.DispClasses); err != nil {
		return SettingsRow{}, false, fmt.Errorf("store: parse displacement_classes: %w", err)
	}
	return out, true, nil
}

// SeedEvent creates the (single) settings row plus the driver/drivetrain
// class_defs rows, in one transaction. This is the "event creation" act
// (plan/DESIGN.md §2.1 setup flow) and must only ever run once against a
// fresh database — calling it again fails on the settings PRIMARY KEY.
func (s *Store) SeedEvent(set SettingsRow, driverClasses, dtClasses []string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	coefJSON, err := json.Marshal(set.Coef)
	if err != nil {
		return fmt.Errorf("store: seed event: marshal coefficients: %w", err)
	}
	dispJSON, err := json.Marshal(set.DispClasses)
	if err != nil {
		return fmt.Errorf("store: seed event: marshal displacement_classes: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: seed event: begin: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO settings (id, event_name, timing_mode, pt_mode, pt_penalty_ms, heat_ranking,
		registration_mode, registration_open, queue_self_entry, max_course_time_sec, sensor_lockout_ms,
		coefficients, displacement_classes) VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		set.EventName, set.TimingMode, set.PTMode, set.PTPenaltyMS, boolToInt(set.HeatRanking),
		set.RegistrationMode, boolToInt(set.RegistrationOpen), boolToInt(set.QueueSelfEntry),
		set.MaxCourseTimeSec, set.SensorLockoutMS, string(coefJSON), string(dispJSON))
	if err != nil {
		return fmt.Errorf("store: seed event: insert settings: %w", err)
	}

	if err := insertClassDefs(tx, "driver", driverClasses); err != nil {
		return err
	}
	if err := insertClassDefs(tx, "drivetrain", dtClasses); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: seed event: commit: %w", err)
	}
	return nil
}

func insertClassDefs(tx *sql.Tx, axis string, labels []string) error {
	stmt, err := tx.Prepare(`INSERT INTO class_defs (axis, label, sort_order) VALUES (?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("store: seed event: prepare class_defs(%s): %w", axis, err)
	}
	defer stmt.Close()
	for i, label := range labels {
		if _, err := stmt.Exec(axis, label, i); err != nil {
			return fmt.Errorf("store: seed event: insert class_defs(%s): %w", axis, err)
		}
	}
	return nil
}

// UpdateSettings overwrites the settings row. Called whenever an admin
// changes event configuration; the web layer re-broadcasts the "settings"
// SSE topic afterwards.
func (s *Store) UpdateSettings(set SettingsRow) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	coefJSON, err := json.Marshal(set.Coef)
	if err != nil {
		return fmt.Errorf("store: update settings: marshal coefficients: %w", err)
	}
	dispJSON, err := json.Marshal(set.DispClasses)
	if err != nil {
		return fmt.Errorf("store: update settings: marshal displacement_classes: %w", err)
	}

	_, err = s.db.Exec(`UPDATE settings SET event_name=?, timing_mode=?, pt_mode=?, pt_penalty_ms=?,
		heat_ranking=?, registration_mode=?, registration_open=?, queue_self_entry=?, max_course_time_sec=?,
		sensor_lockout_ms=?, coefficients=?, displacement_classes=? WHERE id=1`,
		set.EventName, set.TimingMode, set.PTMode, set.PTPenaltyMS, boolToInt(set.HeatRanking),
		set.RegistrationMode, boolToInt(set.RegistrationOpen), boolToInt(set.QueueSelfEntry),
		set.MaxCourseTimeSec, set.SensorLockoutMS, string(coefJSON), string(dispJSON))
	if err != nil {
		return fmt.Errorf("store: update settings: %w", err)
	}
	return nil
}

// ClassDef mirrors a class_defs row.
type ClassDef struct {
	ID        int64
	Axis      string
	Label     string
	SortOrder int
}

// ListClassDefs returns class_defs rows for axis ("driver" or
// "drivetrain"), or all of them (ordered by axis then sort_order) when axis
// is "".
func (s *Store) ListClassDefs(axis string) ([]ClassDef, error) {
	var rows *sql.Rows
	var err error
	if axis == "" {
		rows, err = s.db.Query(`SELECT id, axis, label, sort_order FROM class_defs ORDER BY axis, sort_order, id`)
	} else {
		rows, err = s.db.Query(`SELECT id, axis, label, sort_order FROM class_defs WHERE axis = ? ORDER BY sort_order, id`, axis)
	}
	if err != nil {
		return nil, fmt.Errorf("store: list class_defs: %w", err)
	}
	defer rows.Close()

	var out []ClassDef
	for rows.Next() {
		var c ClassDef
		if err := rows.Scan(&c.ID, &c.Axis, &c.Label, &c.SortOrder); err != nil {
			return nil, fmt.Errorf("store: list class_defs: scan: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list class_defs: %w", err)
	}
	return out, nil
}
