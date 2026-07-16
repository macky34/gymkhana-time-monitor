package web

import (
	"net/http"

	"timemon/internal/store"
)

// handleAPIArchiveEvents implements GET /api/archive/events: every closed
// event, newest first. Public (no auth) — the archive is meant to be
// shareable outside the admin surface.
func (s *Server) handleAPIArchiveEvents(w http.ResponseWriter, r *http.Request) {
	events, err := s.Store.ListEvents()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]adminEventListItem, 0, len(events))
	for _, e := range events {
		if e.Status != "closed" {
			continue
		}
		out = append(out, toAdminEventListItem(e))
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// closedEventOr404 resolves id, writing a bare 404 unless it exists and is
// closed — the archive only ever exposes finished events (the active event,
// if any, is reached through /api/ranking, /api/recent instead).
func (s *Server) closedEventOr404(w http.ResponseWriter, r *http.Request, id int64) (store.EventRow, bool) {
	ev, ok, err := s.Store.GetEvent(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return store.EventRow{}, false
	}
	if !ok || ev.Status != "closed" {
		http.NotFound(w, r)
		return store.EventRow{}, false
	}
	return ev, true
}

// handleAPIArchiveRanking implements GET /api/archive/{id}/ranking.
func (s *Server) handleAPIArchiveRanking(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, ok := s.closedEventOr404(w, r, id); !ok {
		return
	}
	payload, err := s.Snap.RankingPayloadFor(id)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// handleAPIArchiveRecent implements GET /api/archive/{id}/recent (fixed
// 1000-row cap — the archive has no pagination UI).
func (s *Server) handleAPIArchiveRecent(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if _, ok := s.closedEventOr404(w, r, id); !ok {
		return
	}
	payload, err := s.Snap.RecentPayloadFor(id, 1000)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, payload)
}
