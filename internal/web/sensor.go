package web

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"timemon/internal/sse"
	"timemon/internal/store"
	"timemon/internal/timing"
)

// SensorController exposes the course manager as a timing.CourseController so
// main.go can hand it to timing.Listen. The FIFO pairing rules
// (the Sensor-Device wiki page) live on the course manager below.
func (s *Server) SensorController() timing.CourseController { return s.course }

// Orphan-run warning kinds recorded internally by SensorStart/SensorGoal
// (distinct from timing.orphanKindStart/orphanKindGoal, which timing.go
// still uses for the single remaining ErrNoTarget case: a goal trigger with
// no RUNNING car and no queued orphan run to pair with).
const (
	orphanKindStartExpired    = "orphan_start_expired"     // start trigger never got a goal within max_course_time_sec
	orphanKindGoalBeforeStart = "orphan_goal_before_start" // goal trigger's timestamp precedes the orphan run's start
	orphanKindUnassignedLog   = "unassigned_log"           // start+goal orphan pairing produced an unassigned log
)

// sensorTimeHHMMSS renders a sensor-timescale microsecond timestamp as a
// wall-clock HH:MM:SS string for operator-facing orphan warnings.
func sensorTimeHHMMSS(us int64) string {
	return time.UnixMicro(us).Local().Format("15:04:05")
}

// formatElapsedMS renders an elapsed duration (milliseconds) as m:ss.mmm, for
// the "unassigned run recorded" warning detail.
func formatElapsedMS(ms int) string {
	if ms < 0 {
		ms = 0
	}
	minutes := ms / 60000
	rem := ms % 60000
	seconds := rem / 1000
	millis := rem % 1000
	return fmt.Sprintf("%d:%02d.%03d", minutes, seconds, millis)
}

// SensorStart pairs a start-sensor trigger with the oldest on_course car that
// has not yet been stamped (READY), turning it into RUNNING. If there is no
// such car, the trigger becomes an "orphan run" queued for a later goal
// trigger (or admin adoption via /api/admin/course/adopt-orphan) to pair
// with, and nil is returned - there is no longer an ErrNoTarget case for a
// start trigger once an event is active.
func (cm *courseManager) SensorStart(tUS int64) error {
	ev, ok, err := cm.s.Store.GetActiveEvent()
	if err != nil {
		return err
	}
	if !ok {
		return timing.ErrNoTarget
	}
	onCourse, err := cm.s.Store.ListQueue(ev.ID, "on_course") // id asc = oldest first
	if err != nil {
		return err
	}
	for _, row := range onCourse {
		if row.TStartUS != nil {
			continue // already running
		}
		start := tUS
		if err := cm.s.Store.SetStart(row.ID, &start); err != nil {
			return err
		}
		cm.s.publishOnCourse()
		return nil
	}

	// No READY car: queue this as an orphan run. Any orphan runs that have
	// been waiting past max_course_time_sec are pruned first and reported as
	// expired warnings.
	cm.mu.Lock()
	expired := cm.pruneOrphanRunsLocked(tUS, ev.MaxCourseTimeSec)
	cm.orphanSeq++
	cm.orphanRuns = append(cm.orphanRuns, orphanRun{
		ID:       cm.orphanSeq,
		TStartUS: tUS,
		AtMS:     time.Now().UnixMilli(),
	})
	cm.mu.Unlock()

	cm.s.reportExpiredOrphanRuns(expired)
	cm.s.publishOrphans()
	return nil
}

// SensorGoal pairs a goal-sensor trigger with the oldest RUNNING car (start
// stamped, not already inside a finish grace window), finalizing it with
// source="sensor". If no car is running, it instead tries to pair with the
// oldest queued orphan run (a start trigger that had no READY car): that
// produces an unassigned log (driver_id/vehicle_id NULL) the operator can
// later attach via PUT /api/admin/logs/{id}/assign. Returns timing.ErrNoTarget
// only when neither a running car nor a queued orphan run is available (or
// no event is currently active).
func (cm *courseManager) SensorGoal(tUS int64) error {
	ev, ok, err := cm.s.Store.GetActiveEvent()
	if err != nil {
		return err
	}
	if !ok {
		return timing.ErrNoTarget
	}
	onCourse, err := cm.s.Store.ListQueue(ev.ID, "on_course") // id asc = oldest first
	if err != nil {
		return err
	}
	for _, row := range onCourse {
		if row.TStartUS == nil || cm.isPending(row.ID) {
			continue
		}
		return cm.finishCar(row, tUS, "sensor")
	}

	// No RUNNING car: try to pair with the oldest queued orphan run.
	cm.mu.Lock()
	expired := cm.pruneOrphanRunsLocked(tUS, ev.MaxCourseTimeSec)
	var run orphanRun
	haveRun := false
	if len(cm.orphanRuns) > 0 {
		run = cm.orphanRuns[0]
		cm.orphanRuns = cm.orphanRuns[1:]
		haveRun = true
	}
	cm.mu.Unlock()

	cm.s.reportExpiredOrphanRuns(expired)

	if !haveRun {
		if len(expired) > 0 {
			cm.s.publishOrphans()
		}
		return timing.ErrNoTarget
	}

	rawMS := (tUS - run.TStartUS) / 1000
	if rawMS < 0 {
		cm.s.orphans.add(orphanKindGoalBeforeStart,
			fmt.Sprintf("ゴール反応 %s がスタート反応より前のため破棄", sensorTimeHHMMSS(tUS)), nil)
		cm.s.publishOrphans()
		return nil
	}

	logID, err := cm.s.Store.InsertLog(store.LogRow{
		EventID:     ev.ID,
		DriverID:    nil,
		VehicleID:   nil,
		RawMS:       int(rawMS),
		TimestampMS: time.Now().UnixMilli(),
		Source:      "sensor",
	})
	if err != nil {
		// The orphan run must not be silently dropped on a store failure:
		// put it back at the front of the FIFO so a later goal can retry.
		// Republish so SSE clients don't keep a snapshot with it missing.
		cm.restoreOrphanRun(run)
		cm.s.publishOrphans()
		return err
	}

	cm.s.orphans.add(orphanKindUnassignedLog,
		fmt.Sprintf("未割当の走行を記録 %s (スタート %s)", formatElapsedMS(int(rawMS)), sensorTimeHHMMSS(run.TStartUS)),
		&logID)
	cm.s.publishOrphans()
	return nil
}

// reportExpiredOrphanRuns records one orphan_start_expired warning per
// pruned entry. It does not publish - callers batch that with whatever else
// they changed via a single Server.publishOrphans call.
func (s *Server) reportExpiredOrphanRuns(expired []orphanRun) {
	for _, e := range expired {
		s.orphans.add(orphanKindStartExpired,
			fmt.Sprintf("スタート反応 %s がゴール未到達のまま失効しました", sensorTimeHHMMSS(e.TStartUS)), nil)
	}
}

// orphanTracker accumulates unresolved orphan warnings (sensor triggers with
// no pairing target, expired orphan runs, unassigned logs, ...) so the
// admin-only "orphan" SSE topic can render the current set. Entries are
// cleared when the operator resolves them (assigning/deleting the
// associated log, or explicitly dismissing the warning).
type orphanTracker struct {
	mu     sync.Mutex
	items  []orphanItem
	nextID int64
}

type orphanItem struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	AtMS   int64  `json:"at_ms"`
	Detail string `json:"detail"`
	LogID  *int64 `json:"log_id,omitempty"`
}

// add appends a new orphan warning item. Callers are responsible for
// republishing the orphan topic (Server.publishOrphans) afterward.
func (t *orphanTracker) add(kind, detail string, logID *int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextID++
	t.items = append(t.items, orphanItem{
		ID:     t.nextID,
		Kind:   kind,
		AtMS:   time.Now().UnixMilli(),
		Detail: detail,
		LogID:  logID,
	})
}

// remove deletes the item with the given id. ok=false if no such item exists.
func (t *orphanTracker) remove(id int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, it := range t.items {
		if it.ID == id {
			t.items = append(t.items[:i], t.items[i+1:]...)
			return true
		}
	}
	return false
}

// removeByLogID deletes the item (if any) referencing logID. Called after a
// previously-unassigned log is assigned or deleted, so its warning stops
// showing as unresolved. ok=false if no item referenced that log.
func (t *orphanTracker) removeByLogID(logID int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i, it := range t.items {
		if it.LogID != nil && *it.LogID == logID {
			t.items = append(t.items[:i], t.items[i+1:]...)
			return true
		}
	}
	return false
}

// clear removes every item and returns how many were removed.
func (t *orphanTracker) clear() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := len(t.items)
	t.items = nil
	return n
}

// snapshot returns a copy of the current items for JSON marshaling.
func (t *orphanTracker) snapshot() []orphanItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]orphanItem, len(t.items))
	copy(out, t.items)
	return out
}

// OnOrphan is the timing.Deps.OnOrphan callback: it records the warning and
// republishes the orphan topic.
func (s *Server) OnOrphan(kind, detail string) {
	s.orphans.add(kind, detail, nil)
	s.publishOrphans()
}

// publishOrphans republishes the full "orphan" SSE snapshot: both the
// unresolved warning list (orphanTracker) and the live orphan-run FIFO
// (courseManager.orphanRuns), so the admin page can render both the warning
// log and the "adopt this stray start trigger" list. Every mutation of
// either the tracker or the orphan-run FIFO must be followed by a call to
// this method.
func (s *Server) publishOrphans() {
	items := s.orphans.snapshot()
	runs := s.course.orphanRunsSnapshot()
	runsOut := make([]orphanRunOut, 0, len(runs))
	for _, r := range runs {
		runsOut = append(runsOut, orphanRunOut{
			OrphanID: r.ID,
			TStartUS: r.TStartUS,
			AtMS:     r.AtMS,
		})
	}

	data, err := json.Marshal(map[string]any{"items": items, "runs": runsOut})
	if err != nil {
		log.Printf("web: marshal orphan snapshot: %v", err)
		return
	}
	s.Hub.Publish(sse.TopicOrphan, data)
}

// orphanRunOut is the "runs" element shape of the "orphan" SSE topic (see
// publishOrphans). Field names are a wire contract with the admin frontend.
type orphanRunOut struct {
	OrphanID int64 `json:"orphan_id"`
	TStartUS int64 `json:"t_start_us"`
	AtMS     int64 `json:"at_ms"`
}

// OnSensorStatus is the timing.Deps.OnSensorStatus callback: it forwards the
// sensor_status JSON verbatim to the SSE topic of the same name.
func (s *Server) OnSensorStatus(data []byte) {
	s.Hub.Publish(sse.TopicSensorStatus, data)
}
