package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"timemon/internal/store"
)

// ---------------------------------------------------------------------
// shared error / response helpers
// ---------------------------------------------------------------------

// apiError carries an HTTP status alongside a message so a single error
// return value from courseManager / store calls can drive the correct
// response code from a handler.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func conflictf(format string, args ...any) error {
	return &apiError{status: http.StatusConflict, msg: fmt.Sprintf(format, args...)}
}

// writeErr maps an error to an HTTP response: apiError values use their
// carried status, everything else (store/DB failures) becomes a 500.
func writeErr(w http.ResponseWriter, err error) {
	var ae *apiError
	if errors.As(err, &ae) {
		writeJSONError(w, ae.status, ae.msg)
		return
	}
	writeJSONError(w, http.StatusInternalServerError, err.Error())
}

// withAdmin requires a valid session belonging to an admin driver. It
// mirrors withAuth (middleware.go) but additionally checks Role, following
// the same admin-check pattern already used for the SSE stream handler in
// Routes() ("d.Role == \"admin\"").
func (s *Server) withAdmin(next func(w http.ResponseWriter, r *http.Request, d store.Driver)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.driverFromRequest(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if d.Role != "admin" {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r, d)
	}
}

// ---------------------------------------------------------------------
// publish helpers (best-effort, logged not surfaced - the DB mutation
// already succeeded by the time these are called)
// ---------------------------------------------------------------------

func (s *Server) publishQueue() {
	if err := s.Snap.PublishQueue(s.Hub); err != nil {
		log.Printf("web: publish queue failed: %v", err)
	}
}

func (s *Server) publishOnCourse() {
	if err := s.Snap.PublishOnCourse(s.Hub); err != nil {
		log.Printf("web: publish on_course failed: %v", err)
	}
}

func (s *Server) publishRanking() {
	if err := s.Snap.PublishRanking(s.Hub); err != nil {
		log.Printf("web: publish ranking failed: %v", err)
	}
}

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
// course manager
// ---------------------------------------------------------------------

// pendingFinish tracks a car that has crossed the finish line but is still
// within its confirmation grace window: the raw timing log has already
// been written (so a crash cannot lose the timing), but the queue row
// itself stays "on_course" until the grace period elapses (or the
// operator undoes the goal).
type pendingFinish struct {
	queueID int64
	logID   int64
	finMS   int
	untilMS int64
	timer   *time.Timer
}

// courseManager owns the finish-confirmation grace window bookkeeping that
// sits on top of the plain waiting/on_course/done/canceled queue state
// machine persisted in SQLite via store.Store.
type courseManager struct {
	mu      sync.Mutex
	pending map[int64]*pendingFinish
	s       *Server
	graceMS int64
}

// newCourseManager builds a courseManager wired to s. graceMS defaults to
// 3000 (3s); tests may lower it for speed before any finish is recorded.
// NewServer registers cm.finishProvider with the snapshot builder
// (Builder.SetFinishProvider) so OnCourse snapshots can embed in-flight
// finish info.
func newCourseManager(s *Server) *courseManager {
	return &courseManager{
		pending: make(map[int64]*pendingFinish),
		s:       s,
		graceMS: 3000,
	}
}

// finishProvider reports the in-flight finish (if any) for queueID. It is
// registered with the snapshot builder (SetFinishProvider) so OnCourse
// snapshots render "finish":{"fin_ms":...,"until_ms":...} for cars whose
// finish is still inside the confirmation grace window.
func (cm *courseManager) finishProvider(queueID int64) (finMS int, untilMS int64, ok bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	pf, found := cm.pending[queueID]
	if !found {
		return 0, 0, false
	}
	return pf.finMS, pf.untilMS, true
}

func (cm *courseManager) isPending(queueID int64) bool {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	_, ok := cm.pending[queueID]
	return ok
}

// finishCar records a finish for row, which must currently be on_course
// with a start time set (RUNNING) and not already pending. The timing log
// is written immediately (so it survives a crash) and the queue row is
// flipped to "done" only after graceMS elapses, giving the operator a
// short undo-goal window.
func (cm *courseManager) finishCar(row store.QueueRow, tGoalUS int64, source string) error {
	if row.Status != "on_course" {
		return conflictf("queue row %d is not on course", row.ID)
	}
	if row.TStartUS == nil {
		return conflictf("queue row %d has not started", row.ID)
	}
	if cm.isPending(row.ID) {
		return conflictf("queue row %d already has a finish pending", row.ID)
	}

	rawMS := (tGoalUS - *row.TStartUS) / 1000
	if rawMS < 0 {
		return conflictf("finish time precedes start time for queue row %d", row.ID)
	}

	driverID := row.DriverID
	vehicleID := row.VehicleID
	logID, err := cm.s.Store.InsertLog(store.LogRow{
		DriverID:    &driverID,
		VehicleID:   &vehicleID,
		RawMS:       int(rawMS),
		PTCount:     row.PTCount,
		IsMC:        row.MCFlag,
		TimestampMS: time.Now().UnixMilli(),
		Source:      source,
	})
	if err != nil {
		return err
	}

	untilMS := time.Now().UnixMilli() + cm.graceMS

	cm.mu.Lock()
	pf := &pendingFinish{
		queueID: row.ID,
		logID:   logID,
		finMS:   int(rawMS),
		untilMS: untilMS,
	}
	cm.pending[row.ID] = pf
	// Both the map entry and the timer field are set while still holding
	// the lock so that even if graceMS is tiny (tests) and the timer
	// fires "immediately", confirm() - which itself locks cm.mu first -
	// can never observe a partially-initialized pendingFinish.
	pf.timer = time.AfterFunc(time.Duration(cm.graceMS)*time.Millisecond, func() {
		cm.confirm(row.ID)
	})
	cm.mu.Unlock()

	cm.s.publishOnCourse()
	cm.s.publishQueue()

	return nil
}

// confirm finalizes a finish once the grace window has elapsed: the queue
// row moves to "done" and the ranking snapshot picks up the new result.
// Safe to call more than once (e.g. once from the real timer and once
// manually from a test): if the pending entry is already gone - because
// undo-goal removed it, or a previous confirm already ran - this is a
// no-op.
func (cm *courseManager) confirm(queueID int64) {
	cm.mu.Lock()
	_, ok := cm.pending[queueID]
	if ok {
		delete(cm.pending, queueID)
	}
	cm.mu.Unlock()
	if !ok {
		return
	}

	if err := cm.s.Store.SetQueueStatus(queueID, "done"); err != nil {
		log.Printf("web: course confirm SetQueueStatus failed queue=%d: %v", queueID, err)
		return
	}

	cm.s.publishOnCourse()
	cm.s.publishQueue()
	cm.s.publishRanking()
}

// undoGoal reverses a pending finish: the tentative log row is hard
// deleted and the queue row keeps running (still on_course, t_start
// untouched). Only valid while the grace window has not yet elapsed
// (i.e. the queueID is still in the pending map).
func (cm *courseManager) undoGoal(queueID int64) error {
	cm.mu.Lock()
	pf, ok := cm.pending[queueID]
	if !ok {
		cm.mu.Unlock()
		return conflictf("queue row %d has no pending finish", queueID)
	}
	delete(cm.pending, queueID)
	cm.mu.Unlock()

	pf.timer.Stop()

	if err := cm.s.Store.HardDeleteLog(pf.logID); err != nil {
		return err
	}

	cm.s.publishOnCourse()
	cm.s.publishRanking()

	return nil
}

// undoStart reverses a launch: the car leaves the course and returns to
// the front of the waiting queue with PT/MC reset to zero/false. Valid for
// any on_course row (READY or RUNNING) as long as it is not currently in
// the finish grace window.
func (cm *courseManager) undoStart(row store.QueueRow) error {
	if row.Status != "on_course" {
		return conflictf("queue row %d is not on course", row.ID)
	}
	if cm.isPending(row.ID) {
		return conflictf("queue row %d has a finish pending, undo the goal first", row.ID)
	}

	if err := cm.s.Store.SetStart(row.ID, nil); err != nil {
		return err
	}
	if err := cm.s.Store.SetQueueStatus(row.ID, "waiting"); err != nil {
		return err
	}

	pos, err := cm.s.frontOfWaitingPosition()
	if err != nil {
		return err
	}
	if err := cm.s.Store.Reorder(row.ID, pos); err != nil {
		return err
	}

	if row.PTCount != 0 {
		if _, err := cm.s.Store.SetPT(row.ID, -row.PTCount); err != nil {
			return err
		}
	}
	if row.MCFlag {
		if err := cm.s.Store.SetMC(row.ID, false); err != nil {
			return err
		}
	}

	cm.s.publishQueue()
	cm.s.publishOnCourse()

	return nil
}

// cancel aborts an on_course run with no timing log produced. Not allowed
// while a finish is pending (undo-goal must happen first).
func (cm *courseManager) cancel(row store.QueueRow) error {
	if row.Status != "on_course" {
		return conflictf("queue row %d is not on course", row.ID)
	}
	if cm.isPending(row.ID) {
		return conflictf("queue row %d has a finish pending, undo the goal first", row.ID)
	}

	if err := cm.s.Store.SetQueueStatus(row.ID, "canceled"); err != nil {
		return err
	}

	cm.s.publishQueue()
	cm.s.publishOnCourse()

	return nil
}

// frontOfWaitingPosition returns a position value smaller than the current
// head of the waiting queue (or 0 if the queue is empty), suitable for
// reinserting an undone-start car at the very front of "waiting".
func (s *Server) frontOfWaitingPosition() (float64, error) {
	waiting, err := s.Store.ListQueue("waiting")
	if err != nil {
		return 0, err
	}
	if len(waiting) == 0 {
		return 0, nil
	}
	return waiting[0].Position - 1.0, nil
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

	settings, ok, err := s.Store.GetSettings()
	if err != nil {
		writeErr(w, err)
		return
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "event not configured")
		return
	}

	waiting, err := s.Store.ListQueue("waiting")
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

	onCourse, err := s.Store.ListQueue("on_course")
	if err != nil {
		writeErr(w, err)
		return
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
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
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
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
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
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
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
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
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
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body ptBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
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
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
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

// ---------------------------------------------------------------------
// admin: queue management handlers
// ---------------------------------------------------------------------

type queueAddBody struct {
	DriverID  int64 `json:"driver_id"`
	VehicleID int64 `json:"vehicle_id"`
}

// handleAdminQueueAdd implements POST /api/admin/queue.
func (s *Server) handleAdminQueueAdd(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	var body queueAddBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}

	adminID := admin.ID
	queueID, err := s.Store.Enqueue(body.DriverID, body.VehicleID, &adminID)
	if err != nil {
		if errors.Is(err, store.ErrAlreadyWaiting) {
			writeJSONError(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, err)
		return
	}

	s.publishQueue()

	s.audit(&admin.ID, "queue.add", map[string]any{"queue_id": queueID, "driver_id": body.DriverID, "vehicle_id": body.VehicleID})

	writeJSON(w, http.StatusOK, map[string]any{"id": queueID})
}

type queueReorderBody struct {
	Position float64 `json:"position"`
}

// handleAdminQueueReorder implements PUT /api/admin/queue/{id}.
func (s *Server) handleAdminQueueReorder(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body queueReorderBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
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
	if row.Status != "waiting" {
		writeJSONError(w, http.StatusConflict, "queue row is not waiting")
		return
	}

	if err := s.Store.Reorder(id, body.Position); err != nil {
		writeErr(w, err)
		return
	}

	s.publishQueue()

	s.audit(&admin.ID, "queue.reorder", map[string]any{"queue_id": id, "position": body.Position})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleAdminQueueCancel implements DELETE /api/admin/queue/{id}: cancels a
// waiting entry (not yet on course).
func (s *Server) handleAdminQueueCancel(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
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
	if row.Status != "waiting" {
		writeJSONError(w, http.StatusConflict, "queue row is not waiting")
		return
	}

	if err := s.Store.SetQueueStatus(id, "canceled"); err != nil {
		writeErr(w, err)
		return
	}

	s.publishQueue()

	s.audit(&admin.ID, "queue.cancel", map[string]any{"queue_id": id})

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
