package web

import (
	"errors"
	"net/http"

	"timemon/internal/sse"
	"timemon/internal/store"
	"timemon/internal/timing"
)

// handleAdminSensors implements GET /api/admin/sensors: returns the latest
// published sensor-status snapshot verbatim, or an empty list if the hub
// has not published one yet (e.g. no sensors configured/connected).
func (s *Server) handleAdminSensors(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	if b, ok := s.Hub.Snapshot(sse.TopicSensorStatus); ok {
		writeRawJSON(w, http.StatusOK, b)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sensors": []any{}})
}

// handleAdminSensorDelete implements DELETE /api/admin/sensors/{id}: asks
// the timing dispatcher to forget a sensor's in-memory heartbeat/health
// state (last_seen/loss_rate/ntp_offset), so it drops out of the next
// sensor_status payload until it reports again. This never touches
// sensor_events (the UDP dedup ledger) or past logs. Refuses with 409 if
// the sensor has reported within timing.DefaultUnresponsiveAfter, matching
// the "無応答" threshold the admin UI uses to decide when to show the
// delete button.
func (s *Server) handleAdminSensorDelete(w http.ResponseWriter, r *http.Request, admin store.Driver) {
	id := r.PathValue("id")
	if !timing.ValidSensorID(id) {
		writeJSONError(w, http.StatusBadRequest, "unknown sensor_id")
		return
	}
	if s.sensorControl == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "sensor ingest not running")
		return
	}

	err := s.sensorControl.ForgetSensor(id)
	switch {
	case err == nil:
		s.audit(&admin.ID, "admin.sensor.forget", map[string]any{"sensor_id": id})
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, timing.ErrSensorAlive):
		writeJSONError(w, http.StatusConflict, "sensor is still responding")
	case errors.Is(err, timing.ErrSensorUnknown):
		writeJSONError(w, http.StatusNotFound, "sensor not tracked")
	case errors.Is(err, timing.ErrControlUnavailable):
		writeJSONError(w, http.StatusServiceUnavailable, "sensor ingest not running")
	default:
		writeErr(w, err)
	}
}
