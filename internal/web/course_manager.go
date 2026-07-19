// Package web: courseManager, the finish-confirmation grace-window state
// machine layered on top of the plain waiting/on_course/done/canceled queue
// state persisted via store.Store.
package web

import (
	"log"
	"sync"
	"time"

	"timemon/internal/store"
)

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

// orphanRun is a start-sensor trigger that had no READY car to pair with: an
// unassigned run presumed in progress until a goal trigger, admin adoption,
// or expiry (SensorStart/SensorGoal.pruneOrphanRunsLocked) consumes it.
type orphanRun struct {
	ID       int64 // monotonic, for adopt/dismiss APIs
	TStartUS int64 // sensor-timescale timestamp (µs)
	AtMS     int64 // wall-clock receipt time, for display
}

// courseManager owns the finish-confirmation grace window bookkeeping that
// sits on top of the plain waiting/on_course/done/canceled queue state
// machine persisted in SQLite via store.Store, plus the FIFO of orphan start
// triggers awaiting a goal pairing or admin resolution.
type courseManager struct {
	mu         sync.Mutex
	pending    map[int64]*pendingFinish
	orphanRuns []orphanRun // FIFO, oldest first
	orphanSeq  int64
	s          *Server
	graceMS    int64
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

// pruneOrphanRunsLocked removes orphanRuns entries whose elapsed time (from
// TStartUS to refUS, both in the sensor microsecond timescale - never mixed
// with wall-clock time) exceeds maxCourseTimeSec, and returns the removed
// entries so the caller can turn them into orphan warnings once cm.mu is
// released. Callers must already hold cm.mu.
func (cm *courseManager) pruneOrphanRunsLocked(refUS int64, maxCourseTimeSec int) []orphanRun {
	maxUS := int64(maxCourseTimeSec) * 1_000_000
	var expired []orphanRun
	kept := cm.orphanRuns[:0]
	for _, e := range cm.orphanRuns {
		if refUS-e.TStartUS > maxUS {
			expired = append(expired, e)
		} else {
			kept = append(kept, e)
		}
	}
	cm.orphanRuns = kept
	return expired
}

// takeOrphanRun removes and returns the orphan run with the given id (used
// by both the adopt-orphan and dismiss-orphan-run admin APIs). ok=false if
// id is not currently queued: already paired with a goal trigger, expired,
// or never existed.
func (cm *courseManager) takeOrphanRun(id int64) (orphanRun, bool) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	for i, e := range cm.orphanRuns {
		if e.ID == id {
			cm.orphanRuns = append(cm.orphanRuns[:i], cm.orphanRuns[i+1:]...)
			return e, true
		}
	}
	return orphanRun{}, false
}

// restoreOrphanRun reinserts run, keeping the FIFO ordered by ID. Used to
// roll back a takeOrphanRun when the admin adopt-orphan flow fails partway
// through (the store write after the take errors out).
func (cm *courseManager) restoreOrphanRun(run orphanRun) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	i := 0
	for i < len(cm.orphanRuns) && cm.orphanRuns[i].ID < run.ID {
		i++
	}
	cm.orphanRuns = append(cm.orphanRuns, orphanRun{})
	copy(cm.orphanRuns[i+1:], cm.orphanRuns[i:])
	cm.orphanRuns[i] = run
}

// orphanRunsSnapshot returns a copy of the current orphan-run FIFO, for the
// "orphan" SSE topic (see Server.publishOrphans).
func (cm *courseManager) orphanRunsSnapshot() []orphanRun {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	out := make([]orphanRun, len(cm.orphanRuns))
	copy(out, cm.orphanRuns)
	return out
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
		EventID:     row.EventID,
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

// undoStart reverses a launch, resetting PT/MC to zero/false. Valid for any
// on_course row (READY or RUNNING) as long as it is not currently in the
// finish grace window.
//
// A row that was still READY (t_start unset - never actually started, only
// launched/armed) always goes back to the front of the waiting queue: with
// nothing to "re-arm", cancelling the launch is the only sensible outcome.
// This is also the path handleMyLaunchUndo relies on (mypage's self-launch
// cancel only ever calls undoStart while READY), so it must stay
// mode-independent.
//
// A row that had already started (t_start set - RUNNING) behaves
// differently depending on the mode: in sensor mode it re-arms in place
// (stays on_course, t_start cleared back to READY) rather than returning to
// the queue, since a false or early sensor trigger shouldn't cost the
// driver their spot on course and the sensor firing again is exactly what's
// expected to happen next. Manual mode has no such false-trigger scenario
// (the operator typed the start themselves), so there undo still means
// "put this car back at the head of the waiting queue".
func (cm *courseManager) undoStart(row store.QueueRow) error {
	if row.Status != "on_course" {
		return conflictf("queue row %d is not on course", row.ID)
	}
	if cm.isPending(row.ID) {
		return conflictf("queue row %d has a finish pending, undo the goal first", row.ID)
	}
	wasArmed := row.TStartUS == nil

	if err := cm.s.Store.SetStart(row.ID, nil); err != nil {
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

	if !wasArmed {
		ev, ok, err := cm.s.Store.GetEvent(row.EventID)
		if err != nil {
			return err
		}
		if ok && ev.TimingMode == "sensor" {
			cm.s.publishOnCourse()
			return nil
		}
	}

	if err := cm.s.Store.SetQueueStatus(row.ID, "waiting"); err != nil {
		return err
	}

	pos, err := cm.s.frontOfWaitingPosition(row.EventID)
	if err != nil {
		return err
	}
	if err := cm.s.Store.Reorder(row.ID, pos); err != nil {
		return err
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
// head of eventID's waiting queue (or 0 if the queue is empty), suitable for
// reinserting an undone-start car at the very front of "waiting".
func (s *Server) frontOfWaitingPosition(eventID int64) (float64, error) {
	waiting, err := s.Store.ListQueue(eventID, "waiting")
	if err != nil {
		return 0, err
	}
	if len(waiting) == 0 {
		return 0, nil
	}
	return waiting[0].Position - 1.0, nil
}
