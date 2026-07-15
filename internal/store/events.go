package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"timemon/internal/domain"
)

// ErrActiveEventExists is returned by CreateEvent when an active event
// already exists: the events(status) partial unique index only allows one
// 'active' row at a time (plan/DESIGN.md multi-event design), so a second
// concurrent CreateEvent call fails on that constraint.
var ErrActiveEventExists = errors.New("store: an active event already exists")

// ErrEventNotActive is returned by CloseEvent when the target event id does
// not currently have status='active' (already closed, or does not exist).
var ErrEventNotActive = errors.New("store: event is not active")

// EventRow mirrors one events table row: the event's lifecycle (ID, Status,
// CreatedAtMS, ClosedAtMS) plus its configuration (everything else, formerly
// the single-row "settings" table). Coef/DispClasses are parsed from the
// coefficients/displacement_classes JSON columns into domain types.
type EventRow struct {
	ID               int64
	EventName        string
	Status           string // 'active' | 'closed'
	CreatedAtMS      int64
	ClosedAtMS       *int64
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

const eventSelectCols = `id, name, status, created_at_ms, closed_at_ms, timing_mode, pt_mode, pt_penalty_ms, heat_ranking,
	registration_mode, registration_open, queue_self_entry, max_course_time_sec, sensor_lockout_ms,
	coefficients, displacement_classes`

func scanEventRow(row rowScanner) (EventRow, error) {
	var e EventRow
	var closedAt sql.NullInt64
	var heatRanking, regOpen, selfEntry int
	var coefJSON, dispJSON string
	err := row.Scan(&e.ID, &e.EventName, &e.Status, &e.CreatedAtMS, &closedAt, &e.TimingMode, &e.PTMode, &e.PTPenaltyMS,
		&heatRanking, &e.RegistrationMode, &regOpen, &selfEntry, &e.MaxCourseTimeSec, &e.SensorLockoutMS,
		&coefJSON, &dispJSON)
	if err != nil {
		return EventRow{}, err
	}
	if closedAt.Valid {
		v := closedAt.Int64
		e.ClosedAtMS = &v
	}
	e.HeatRanking = heatRanking != 0
	e.RegistrationOpen = regOpen != 0
	e.QueueSelfEntry = selfEntry != 0

	if err := json.Unmarshal([]byte(coefJSON), &e.Coef); err != nil {
		return EventRow{}, fmt.Errorf("store: parse coefficients: %w", err)
	}
	if err := json.Unmarshal([]byte(dispJSON), &e.DispClasses); err != nil {
		return EventRow{}, fmt.Errorf("store: parse displacement_classes: %w", err)
	}
	return e, nil
}

// GetActiveEvent returns the single active event row (status='active'). The
// partial unique index on events(status) guarantees there is at most one.
// ok=false means no event is currently active (fresh DB awaiting first-run
// setup, or every event has been closed) — callers use that to decide
// whether setup mode / "no active event" (409) behavior applies.
func (s *Store) GetActiveEvent() (EventRow, bool, error) {
	row := s.db.QueryRow(`SELECT ` + eventSelectCols + ` FROM events WHERE status = 'active'`)
	e, err := scanEventRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EventRow{}, false, nil
	}
	if err != nil {
		return EventRow{}, false, fmt.Errorf("store: get active event: %w", err)
	}
	return e, true, nil
}

// GetEvent looks up one event by id, active or closed (used by archive
// views that address a specific past event).
func (s *Store) GetEvent(id int64) (EventRow, bool, error) {
	row := s.db.QueryRow(`SELECT `+eventSelectCols+` FROM events WHERE id = ?`, id)
	e, err := scanEventRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return EventRow{}, false, nil
	}
	if err != nil {
		return EventRow{}, false, fmt.Errorf("store: get event: %w", err)
	}
	return e, true, nil
}

// ListEvents returns every event (active and closed), newest first.
func (s *Store) ListEvents() ([]EventRow, error) {
	rows, err := s.db.Query(`SELECT ` + eventSelectCols + ` FROM events ORDER BY created_at_ms DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	defer rows.Close()

	var out []EventRow
	for rows.Next() {
		e, err := scanEventRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: list events: scan: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list events: %w", err)
	}
	return out, nil
}

// CreateEvent inserts a new event with status='active' and created_at_ms=now.
// If an active event already exists, the events(status) partial unique
// index rejects the insert and this returns ErrActiveEventExists (no row
// inserted).
func (s *Store) CreateEvent(set EventRow) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	coefJSON, err := json.Marshal(set.Coef)
	if err != nil {
		return 0, fmt.Errorf("store: create event: marshal coefficients: %w", err)
	}
	dispJSON, err := json.Marshal(set.DispClasses)
	if err != nil {
		return 0, fmt.Errorf("store: create event: marshal displacement_classes: %w", err)
	}

	now := time.Now().UnixMilli()
	res, err := s.db.Exec(`INSERT INTO events (name, status, created_at_ms, timing_mode, pt_mode, pt_penalty_ms,
		heat_ranking, registration_mode, registration_open, queue_self_entry, max_course_time_sec, sensor_lockout_ms,
		coefficients, displacement_classes) VALUES (?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		set.EventName, now, set.TimingMode, set.PTMode, set.PTPenaltyMS, boolToInt(set.HeatRanking),
		set.RegistrationMode, boolToInt(set.RegistrationOpen), boolToInt(set.QueueSelfEntry),
		set.MaxCourseTimeSec, set.SensorLockoutMS, string(coefJSON), string(dispJSON))
	if err != nil {
		if isUniqueConstraintErr(err) {
			return 0, ErrActiveEventExists
		}
		return 0, fmt.Errorf("store: create event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create event: last insert id: %w", err)
	}
	return id, nil
}

// CloseEvent transitions event id from 'active' to 'closed', stamping
// closed_at_ms. Returns ErrEventNotActive (no row changed) if id does not
// currently have status='active'.
func (s *Store) CloseEvent(id int64) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UnixMilli()
	res, err := s.db.Exec(`UPDATE events SET status = 'closed', closed_at_ms = ? WHERE id = ? AND status = 'active'`, now, id)
	if err != nil {
		return fmt.Errorf("store: close event: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: close event: rows affected: %w", err)
	}
	if n == 0 {
		return ErrEventNotActive
	}
	return nil
}

// SeedEvent creates the first event (status='active') plus the driver/
// drivetrain class_defs rows, in one transaction. This is the "event
// creation" act of the first-run setup wizard (plan/DESIGN.md §2.1).
// set.ID/Status/CreatedAtMS/ClosedAtMS are ignored (assigned by this
// insert); if an active event already exists this fails on the
// events(status) partial unique index.
func (s *Store) SeedEvent(set EventRow, driverClasses, dtClasses []string) error {
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

	now := time.Now().UnixMilli()
	_, err = tx.Exec(`INSERT INTO events (name, status, created_at_ms, timing_mode, pt_mode, pt_penalty_ms, heat_ranking,
		registration_mode, registration_open, queue_self_entry, max_course_time_sec, sensor_lockout_ms,
		coefficients, displacement_classes) VALUES (?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		set.EventName, now, set.TimingMode, set.PTMode, set.PTPenaltyMS, boolToInt(set.HeatRanking),
		set.RegistrationMode, boolToInt(set.RegistrationOpen), boolToInt(set.QueueSelfEntry),
		set.MaxCourseTimeSec, set.SensorLockoutMS, string(coefJSON), string(dispJSON))
	if err != nil {
		return fmt.Errorf("store: seed event: insert event: %w", err)
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

// UpdateEvent overwrites event set.ID's configuration columns (everything
// except id/status/created_at_ms/closed_at_ms, which this never touches).
// Called whenever an admin changes event configuration; the web layer
// re-broadcasts the "settings" SSE topic afterwards.
func (s *Store) UpdateEvent(set EventRow) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	coefJSON, err := json.Marshal(set.Coef)
	if err != nil {
		return fmt.Errorf("store: update event: marshal coefficients: %w", err)
	}
	dispJSON, err := json.Marshal(set.DispClasses)
	if err != nil {
		return fmt.Errorf("store: update event: marshal displacement_classes: %w", err)
	}

	_, err = s.db.Exec(`UPDATE events SET name=?, timing_mode=?, pt_mode=?, pt_penalty_ms=?,
		heat_ranking=?, registration_mode=?, registration_open=?, queue_self_entry=?, max_course_time_sec=?,
		sensor_lockout_ms=?, coefficients=?, displacement_classes=? WHERE id=?`,
		set.EventName, set.TimingMode, set.PTMode, set.PTPenaltyMS, boolToInt(set.HeatRanking),
		set.RegistrationMode, boolToInt(set.RegistrationOpen), boolToInt(set.QueueSelfEntry),
		set.MaxCourseTimeSec, set.SensorLockoutMS, string(coefJSON), string(dispJSON), set.ID)
	if err != nil {
		return fmt.Errorf("store: update event: %w", err)
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
