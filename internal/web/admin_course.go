// Package web: admin course-control handlers (launch/finish/cancel/undo/
// PT/MC) plus the client_ms timestamp-correction helper they share.
package web

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"timemon/internal/store"
)

// ---------------------------------------------------------------------
// client_ms correction (manual timing mode)
// ---------------------------------------------------------------------

// correctedMs resolves the timestamp to use for a manual start/finish tap:
// the client-supplied value if present and within 2s of server time,
// otherwise the server's own current time (both in epoch milliseconds).
func correctedMs(clientMS *int64) int64 {
	now := time.Now().UnixMilli()
	if clientMS == nil {
		return now
	}
	diff := now - *clientMS
	if diff < 0 {
		diff = -diff
	}
	if diff > 2000 {
		return now
	}
	return *clientMS
}

type clientMsBody struct {
	ClientMS *int64 `json:"client_ms"`
}

// decodeClientMs reads an optional {"client_ms": ...} body. A missing body,
// empty body, or unparsable JSON simply yields a nil client_ms (server time
// will be used instead) rather than an error, since the field is documented
// as omittable.
func decodeClientMs(r *http.Request) *int64 {
	if r.Body == nil {
		return nil
	}
	var body clientMsBody
	_ = json.NewDecoder(r.Body).Decode(&body)
	return body.ClientMS
}

// ---------------------------------------------------------------------
// admin: course control handlers
// ---------------------------------------------------------------------

// handleAdminCourseLaunch implements POST /api/admin/course: the driver at
// the head of the waiting queue is launched onto the course. In sensor
// timing mode the car starts READY (t_start left NULL, armed by the
// sensor later); in manual mode t_start is set immediately from the
// (corrected) client timestamp.
func (s *Server) handleAdminCourseLaunch(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	clientMS := decodeClientMs(r)

	settings, ok := s.requireActiveEvent(w)
	if !ok {
		return
	}

	waiting, err := s.Store.ListQueue(settings.ID, "waiting")
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(waiting) == 0 {
		writeJSONError(w, http.StatusConflict, "queue is empty")
		return
	}
	row := waiting[0]

	// Compare-and-set: if a concurrent launch/adopt already moved this row
	// out of "waiting", answer 409 instead of double-launching it.
	claimed, err := s.Store.ClaimQueueRow(row.ID, "waiting", "on_course")
	if err != nil {
		writeErr(w, err)
		return
	}
	if !claimed {
		writeJSONError(w, http.StatusConflict, "queue head changed, retry")
		return
	}

	if settings.TimingMode == "sensor" {
		if err := s.Store.SetStart(row.ID, nil); err != nil {
			writeErr(w, err)
			return
		}
	} else {
		tStartUS := correctedMs(clientMS) * 1000
		if err := s.Store.SetStart(row.ID, &tStartUS); err != nil {
			writeErr(w, err)
			return
		}
	}

	s.publishQueue()
	s.publishOnCourse()

	s.audit(&admin.ID, "course.launch", map[string]any{
		"queue_id":   row.ID,
		"driver_id":  row.DriverID,
		"vehicle_id": row.VehicleID,
		"mode":       settings.TimingMode,
	})

	writeJSON(w, http.StatusOK, map[string]any{"queue_id": row.ID})
}

// handleAdminCourseFinishOldest implements POST /api/admin/course/finish:
// finishes the oldest RUNNING (t_start set, not already pending) car on
// course.
func (s *Server) handleAdminCourseFinishOldest(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	clientMS := decodeClientMs(r)
	tGoalUS := correctedMs(clientMS) * 1000

	ev, activeOK, err := s.activeEvent()
	if err != nil {
		writeErr(w, err)
		return
	}
	var onCourse []store.QueueRow
	if activeOK {
		onCourse, err = s.Store.ListQueue(ev.ID, "on_course")
		if err != nil {
			writeErr(w, err)
			return
		}
	}

	var target *store.QueueRow
	for i := range onCourse {
		if onCourse[i].TStartUS == nil {
			continue
		}
		if s.course.isPending(onCourse[i].ID) {
			continue
		}
		target = &onCourse[i]
		break
	}
	if target == nil {
		writeJSONError(w, http.StatusConflict, "no running car to finish")
		return
	}

	if err := s.course.finishCar(*target, tGoalUS, "manual"); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "course.finish", map[string]any{"queue_id": target.ID, "select": "oldest"})

	writeJSON(w, http.StatusOK, map[string]any{"queue_id": target.ID})
}

// handleAdminCourseStartOldest implements POST /api/admin/course/start:
// stamps a manual t_start on the oldest READY (on_course, t_start not yet
// set) car, for use when the start sensor is unavailable or misfires.
func (s *Server) handleAdminCourseStartOldest(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	clientMS := decodeClientMs(r)

	settings, ok := s.requireActiveEvent(w)
	if !ok {
		return
	}

	onCourse, err := s.Store.ListQueue(settings.ID, "on_course")
	if err != nil {
		writeErr(w, err)
		return
	}

	var target *store.QueueRow
	for i := range onCourse {
		if onCourse[i].TStartUS == nil {
			target = &onCourse[i]
			break
		}
	}
	if target == nil {
		writeJSONError(w, http.StatusConflict, "no armed car on course")
		return
	}

	tStartUS := correctedMs(clientMS) * 1000

	// Compare-and-set: if a concurrent sensor trigger already stamped this
	// row first, answer 409 instead of overwriting its start time.
	updated, err := s.Store.SetStartIfUnset(target.ID, tStartUS)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !updated {
		writeJSONError(w, http.StatusConflict, "start already stamped")
		return
	}

	s.publishQueue()
	s.publishOnCourse()

	s.audit(&admin.ID, "course.manual_start", map[string]any{
		"queue_id":   target.ID,
		"driver_id":  target.DriverID,
		"vehicle_id": target.VehicleID,
		"t_start_us": tStartUS,
	})

	writeJSON(w, http.StatusOK, map[string]any{"queue_id": target.ID})
}

// handleAdminCourseFinishByID implements POST /api/admin/course/{id}/finish:
// finishes a specific car, which must currently be RUNNING.
func (s *Server) handleAdminCourseFinishByID(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	clientMS := decodeClientMs(r)
	tGoalUS := correctedMs(clientMS) * 1000

	row, ok, err := s.Store.GetQueueRow(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	if err := s.course.finishCar(row, tGoalUS, "manual"); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "course.finish", map[string]any{"queue_id": row.ID, "select": "by_id"})

	writeJSON(w, http.StatusOK, map[string]any{"queue_id": row.ID})
}

// handleAdminCourseCancel implements DELETE /api/admin/course/{id}: aborts
// an on_course run with no timing log produced (READY or RUNNING, but not
// while a finish is pending).
func (s *Server) handleAdminCourseCancel(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	row, ok, err := s.Store.GetQueueRow(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	if err := s.course.cancel(row); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "course.cancel", map[string]any{"queue_id": row.ID})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminCourseUndoStart implements POST /api/admin/course/{id}/undo-start.
func (s *Server) handleAdminCourseUndoStart(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	row, ok, err := s.Store.GetQueueRow(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	if err := s.course.undoStart(row); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "course.undo_start", map[string]any{"queue_id": row.ID})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminCourseUndoGoal implements POST /api/admin/course/{id}/undo-goal.
func (s *Server) handleAdminCourseUndoGoal(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}

	if err := s.course.undoGoal(id); err != nil {
		writeErr(w, err)
		return
	}

	s.audit(&admin.ID, "course.undo_goal", map[string]any{"queue_id": id})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type ptBody struct {
	Delta int `json:"delta"`
}

type adoptOrphanBody struct {
	OrphanID int64 `json:"orphan_id"`
}

// handleAdminCourseAdoptOrphan implements POST /api/admin/course/adopt-orphan:
// pairs a stray start-sensor trigger (queued as an "orphan run" because no
// READY car was waiting when it fired - typically the operator forgot to
// launch before the sensor triggered) with the driver at the head of the
// waiting queue, exactly as if that car had been launched in sensor mode and
// immediately stamped by the sensor.
func (s *Server) handleAdminCourseAdoptOrphan(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	body, ok := decodeReqJSON[adoptOrphanBody](w, r)
	if !ok {
		return
	}

	ev, ok := s.requireActiveEvent(w)
	if !ok {
		return
	}

	waiting, err := s.Store.ListQueue(ev.ID, "waiting")
	if err != nil {
		writeErr(w, err)
		return
	}
	if len(waiting) == 0 {
		writeJSONError(w, http.StatusConflict, "queue is empty")
		return
	}
	row := waiting[0]

	run, ok := s.course.takeOrphanRun(body.OrphanID)
	if !ok {
		writeJSONError(w, http.StatusConflict, "orphan run already consumed or expired")
		return
	}

	// Compare-and-set so a concurrent launch/adopt that already moved this
	// row out of "waiting" loses cleanly (409 + orphan run restored) instead
	// of double-writing the row and silently losing a start timestamp.
	claimed, err := s.Store.ClaimQueueRow(row.ID, "waiting", "on_course")
	if err != nil {
		s.course.restoreOrphanRun(run)
		s.publishOrphans()
		writeErr(w, err)
		return
	}
	if !claimed {
		s.course.restoreOrphanRun(run)
		s.publishOrphans()
		writeJSONError(w, http.StatusConflict, "queue head changed, retry")
		return
	}
	start := run.TStartUS
	if err := s.Store.SetStart(row.ID, &start); err != nil {
		// Best-effort rollback of the claim; the orphan run itself is always
		// restored so the stamp cannot be lost.
		if _, rbErr := s.Store.ClaimQueueRow(row.ID, "on_course", "waiting"); rbErr != nil {
			log.Printf("web: adopt-orphan rollback failed queue=%d: %v", row.ID, rbErr)
		}
		s.course.restoreOrphanRun(run)
		s.publishOrphans()
		writeErr(w, err)
		return
	}

	s.publishQueue()
	s.publishOnCourse()
	s.publishOrphans()

	s.audit(&admin.ID, "course.adopt_orphan", map[string]any{
		"queue_id":   row.ID,
		"orphan_id":  body.OrphanID,
		"t_start_us": run.TStartUS,
	})

	writeJSON(w, http.StatusOK, map[string]any{"queue_id": row.ID})
}

// handleAdminCourseDismissOrphanRun implements DELETE
// /api/admin/course/orphan-runs/{id}: discards a queued orphan start trigger
// (e.g. a false sensor detection) without ever pairing it with a car.
func (s *Server) handleAdminCourseDismissOrphanRun(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}

	if _, ok := s.course.takeOrphanRun(id); !ok {
		writeJSONError(w, http.StatusConflict, "orphan run already consumed or expired")
		return
	}

	s.publishOrphans()
	s.audit(&admin.ID, "course.dismiss_orphan", map[string]any{"orphan_id": id})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminOrphanDismiss implements DELETE /api/admin/orphans/{id}:
// dismisses a single orphan warning from the admin-facing list, without
// touching any underlying log/queue/orphan-run state.
func (s *Server) handleAdminOrphanDismiss(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}

	if !s.orphans.remove(id) {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}

	s.publishOrphans()
	s.audit(&admin.ID, "admin.orphan.dismiss", map[string]any{"id": id})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminOrphanClear implements DELETE /api/admin/orphans: dismisses
// every orphan warning at once.
func (s *Server) handleAdminOrphanClear(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	n := s.orphans.clear()

	s.publishOrphans()
	s.audit(&admin.ID, "admin.orphan.clear", map[string]any{"count": n})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminCoursePT implements PUT /api/admin/course/{id}/pt.
func (s *Server) handleAdminCoursePT(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	body, ok := decodeReqJSON[ptBody](w, r)
	if !ok {
		return
	}

	row, ok, err := s.Store.GetQueueRow(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if row.Status != "on_course" {
		writeJSONError(w, http.StatusConflict, "queue row is not on course")
		return
	}

	newCount, err := s.Store.SetPT(id, body.Delta)
	if err != nil {
		if errors.Is(err, store.ErrPTBelowZero) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, err)
		return
	}

	s.publishQueue()
	s.publishOnCourse()

	s.audit(&admin.ID, "course.pt", map[string]any{"queue_id": id, "delta": body.Delta, "pt_count": newCount})

	writeJSON(w, http.StatusOK, map[string]any{"pt_count": newCount})
}

// handleAdminCourseMC implements PUT /api/admin/course/{id}/mc (toggle).
func (s *Server) handleAdminCourseMC(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}

	row, ok, err := s.Store.GetQueueRow(id)
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if row.Status != "on_course" {
		writeJSONError(w, http.StatusConflict, "queue row is not on course")
		return
	}

	newFlag := !row.MCFlag
	if err := s.Store.SetMC(id, newFlag); err != nil {
		writeErr(w, err)
		return
	}

	s.publishQueue()
	s.publishOnCourse()

	s.audit(&admin.ID, "course.mc", map[string]any{"queue_id": id, "mc_flag": newFlag})

	writeJSON(w, http.StatusOK, map[string]any{"mc_flag": newFlag})
}
