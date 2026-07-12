package web

import (
	"net/http"

	"timemon/internal/store"
)

// handleMyLaunch implements POST /api/my/queue/launch: a participant at the
// head of the waiting queue launches themselves onto the course. Only
// available in sensor timing mode - the car enters READY (t_start unset)
// and the start sensor supplies the actual start time later. Everything
// else (not at the head, manual timing mode, already on course) is a 409.
func (s *Server) handleMyLaunch(w http.ResponseWriter, r *http.Request, d store.Driver) {
	settings, ok, err := s.Store.GetSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok || settings.TimingMode != "sensor" {
		writeJSONError(w, http.StatusConflict, "self launch requires sensor timing mode")
		return
	}

	onCourse, err := s.Store.ListQueue("on_course")
	if err != nil {
		writeErr(w, err)
		return
	}
	for _, row := range onCourse {
		if row.DriverID == d.ID {
			writeJSONError(w, http.StatusConflict, "already on course")
			return
		}
	}

	waiting, err := s.Store.ListQueue("waiting")
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(waiting) == 0 || waiting[0].DriverID != d.ID {
		writeJSONError(w, http.StatusConflict, "not at the head of the queue")
		return
	}
	row := waiting[0]

	if err := s.Store.SetQueueStatus(row.ID, "on_course"); err != nil {
		writeErr(w, err)
		return
	}
	// READY: t_start stays NULL until the start sensor fires.
	if err := s.Store.SetStart(row.ID, nil); err != nil {
		writeErr(w, err)
		return
	}

	s.publishQueue()
	s.publishOnCourse()

	s.audit(&d.ID, "my.launch", map[string]any{"queue_id": row.ID, "vehicle_id": row.VehicleID})

	writeJSON(w, http.StatusOK, map[string]any{"queue_id": row.ID})
}

// handleMyLaunchUndo implements DELETE /api/my/queue/launch: a participant
// who launched themselves but has not yet crossed the start line (READY,
// t_start still unset) returns to the head of the waiting queue. Once the
// clock is running (t_start set) this is a 409 - only an operator can undo
// a started run.
func (s *Server) handleMyLaunchUndo(w http.ResponseWriter, r *http.Request, d store.Driver) {
	onCourse, err := s.Store.ListQueue("on_course")
	if err != nil {
		writeErr(w, err)
		return
	}

	var mine *store.QueueRow
	for i := range onCourse {
		if onCourse[i].DriverID == d.ID {
			mine = &onCourse[i]
			break
		}
	}
	if mine == nil {
		writeJSONError(w, http.StatusConflict, "not on course")
		return
	}
	if mine.TStartUS != nil {
		writeJSONError(w, http.StatusConflict, "already started")
		return
	}

	if err := s.course.undoStart(*mine); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&d.ID, "my.launch.undo", map[string]any{"queue_id": mine.ID})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
