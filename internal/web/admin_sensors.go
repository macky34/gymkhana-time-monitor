package web

import (
	"net/http"

	"timemon/internal/sse"
	"timemon/internal/store"
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
