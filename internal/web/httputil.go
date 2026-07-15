package web

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"

	"timemon/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeRawJSON writes an already-marshaled JSON payload (e.g. from the
// snapshot builder) directly to the response body.
func writeRawJSON(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func parsePathInt64(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

// etagFor produces a weak-ish content hash ETag: "<hex fnv64a>".
func etagFor(b []byte) string {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return fmt.Sprintf("\"%x\"", h.Sum64())
}

// activeEvent resolves the current active event, if any. ok=false means no
// event is currently active.
func (s *Server) activeEvent() (store.EventRow, bool, error) {
	return s.Store.GetActiveEvent()
}

// requireActiveEvent resolves the active event or writes the shared 409
// "no active event" response and returns ok=false. Handlers whose mutation
// only makes sense against a configured event (Enqueue, event settings
// edits, launching onto the course, ...) call this first and bail out on
// !ok; read-only handlers instead treat "no active event" as empty data.
func (s *Server) requireActiveEvent(w http.ResponseWriter) (store.EventRow, bool) {
	ev, ok, err := s.activeEvent()
	if err != nil {
		writeErr(w, err)
		return store.EventRow{}, false
	}
	if !ok {
		writeJSONError(w, http.StatusConflict, "no active event")
		return store.EventRow{}, false
	}
	return ev, true
}
