// Package web: admin course-control handlers (launch/finish/cancel/undo/
// PT/MC) plus the client_ms timestamp-correction helper they share.
package web

import (
	"encoding/json"
	"errors"
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

	if err := s.Store.SetQueueStatus(row.ID, "on_course"); err != nil {
		writeErr(w, err)
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
