package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"strconv"

	"timemon/internal/store"
)

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

// requirePathID parses the "id" path parameter, writing the shared 400
// "invalid id" JSON error for anything unparsable. Used by handlers that
// look up the row themselves afterward (and so already 404 separately on
// "not found").
func requirePathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// pathID parses the "id" path parameter, writing a bare 404 (no JSON body)
// for anything unparsable - used where an invalid id is meant to look
// identical to an unknown one.
func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := parsePathInt64(r, "id")
	if err != nil {
		http.NotFound(w, r)
		return 0, false
	}
	return id, true
}

// decodeReqJSON decodes r.Body into a T, writing the shared 400 "invalid
// body" JSON error on failure. (Named distinctly from the test-only
// decodeJSON[T](t, body) helper in scenario_test.go, which has a different
// signature.)
func decodeReqJSON[T any](w http.ResponseWriter, r *http.Request) (T, bool) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		var zero T
		return zero, false
	}
	return v, true
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
