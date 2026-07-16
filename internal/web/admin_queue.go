// Package web: admin queue-management handlers (add/reorder/cancel a
// waiting entry).
package web

import (
	"errors"
	"net/http"

	"timemon/internal/store"
)

type queueAddBody struct {
	DriverID  int64 `json:"driver_id"`
	VehicleID int64 `json:"vehicle_id"`
}

// handleAdminQueueAdd implements POST /api/admin/queue.
func (s *Server) handleAdminQueueAdd(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	body, ok := decodeReqJSON[queueAddBody](w, r)
	if !ok {
		return
	}

	ev, ok := s.requireActiveEvent(w)
	if !ok {
		return
	}

	adminID := admin.ID
	queueID, err := s.Store.Enqueue(ev.ID, body.DriverID, body.VehicleID, &adminID)
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
	id, ok := requirePathID(w, r)
	if !ok {
		return
	}
	body, ok := decodeReqJSON[queueReorderBody](w, r)
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
