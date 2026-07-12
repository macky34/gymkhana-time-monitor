package web

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (s *Server) handleAPIRanking(w http.ResponseWriter, r *http.Request) {
	b, err := s.Snap.Ranking()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, b)
}

func (s *Server) handleAPISettings(w http.ResponseWriter, r *http.Request) {
	b, err := s.Snap.Settings()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, b)
}

// handleAPIQueue implements GET /api/queue. Per CONTRACTS this returns
// {"waiting":[...],"on_course":[...]}, assembled from the snapshot
// builder's Queue() ({"items":[...]}) and OnCourse() ({"cars":[...]}).
func (s *Server) handleAPIQueue(w http.ResponseWriter, r *http.Request) {
	waitingB, err := s.Snap.Queue()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	onCourseB, err := s.Snap.OnCourse()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var qWrap struct {
		Items json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(waitingB, &qWrap); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var oWrap struct {
		Cars json.RawMessage `json:"cars"`
	}
	if err := json.Unmarshal(onCourseB, &oWrap); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]json.RawMessage{
		"waiting":   qWrap.Items,
		"on_course": oWrap.Cars,
	})
}

func (s *Server) handleAPIRecent(w http.ResponseWriter, r *http.Request) {
	limit := 10
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 50 {
		limit = 50
	}
	b, err := s.Snap.Recent(limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, b)
}

func (s *Server) handleAPICombinationLogs(w http.ResponseWriter, r *http.Request) {
	d, err1 := parsePathInt64(r, "d")
	v, err2 := parsePathInt64(r, "v")
	if err1 != nil || err2 != nil {
		http.NotFound(w, r)
		return
	}
	b, err := s.Snap.CombinationLogs(d, v, r.URL.Query())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, b)
}
