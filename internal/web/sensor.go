package web

import (
	"encoding/json"
	"log"
	"sync"
	"time"

	"timemon/internal/sse"
	"timemon/internal/timing"
)

// SensorController exposes the course manager as a timing.CourseController so
// main.go can hand it to timing.Listen. The FIFO pairing rules
// (DESIGN.md §7.1) live on the course manager below.
func (s *Server) SensorController() timing.CourseController { return s.course }

// SensorStart pairs a start-sensor trigger with the oldest on_course car that
// has not yet been stamped (READY), turning it into RUNNING. Returns
// timing.ErrNoTarget when there is no such car (an orphan start trigger).
func (cm *courseManager) SensorStart(tUS int64) error {
	onCourse, err := cm.s.Store.ListQueue("on_course") // id asc = oldest first
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
	return timing.ErrNoTarget
}

// SensorGoal pairs a goal-sensor trigger with the oldest RUNNING car (start
// stamped, not already inside a finish grace window), finalizing it with
// source="sensor". Returns timing.ErrNoTarget when no car is running.
func (cm *courseManager) SensorGoal(tUS int64) error {
	onCourse, err := cm.s.Store.ListQueue("on_course") // id asc = oldest first
	if err != nil {
		return err
	}
	for _, row := range onCourse {
		if row.TStartUS == nil || cm.isPending(row.ID) {
			continue
		}
		return cm.finishCar(row, tUS, "sensor")
	}
	return timing.ErrNoTarget
}

// orphanTracker accumulates unresolved orphan warnings (sensor triggers with
// no pairing target) so the admin-only "orphan" SSE topic can render the
// current set. Entries are cleared when the operator resolves them via the
// logs tab (W4); for now the set only grows within a run.
type orphanTracker struct {
	mu    sync.Mutex
	items []orphanItem
}

type orphanItem struct {
	Kind   string `json:"kind"`
	AtMS   int64  `json:"at_ms"`
	Detail string `json:"detail"`
}

// OnOrphan is the timing.Deps.OnOrphan callback: it records the warning and
// republishes the orphan topic.
func (s *Server) OnOrphan(kind, detail string) {
	s.orphans.mu.Lock()
	s.orphans.items = append(s.orphans.items, orphanItem{
		Kind:   kind,
		AtMS:   time.Now().UnixMilli(),
		Detail: detail,
	})
	snapshot := make([]orphanItem, len(s.orphans.items))
	copy(snapshot, s.orphans.items)
	s.orphans.mu.Unlock()

	data, err := json.Marshal(map[string]any{"items": snapshot})
	if err != nil {
		log.Printf("web: marshal orphan snapshot: %v", err)
		return
	}
	s.Hub.Publish(sse.TopicOrphan, data)
}

// OnSensorStatus is the timing.Deps.OnSensorStatus callback: it forwards the
// sensor_status JSON verbatim to the SSE topic of the same name.
func (s *Server) OnSensorStatus(data []byte) {
	s.Hub.Publish(sse.TopicSensorStatus, data)
}
