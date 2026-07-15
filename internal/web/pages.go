package web

import "net/http"

// PageData is the only data every server-rendered page receives. All
// per-page dynamic behavior (ranking table contents, queue state, etc.) is
// fetched client-side from the JSON APIs / SSE stream.
type PageData struct {
	EventName string
	Authed    bool
	IsAdmin   bool
	MyID      int64
	SetupMode bool
}

// pageData builds the PageData for the current request: event name (empty
// before setup) plus whatever auth state the tm_session cookie carries, if
// any. Pages render regardless of auth state.
func (s *Server) pageData(r *http.Request) PageData {
	pd := PageData{}
	if ev, ok, err := s.Store.GetActiveEvent(); err == nil && ok {
		pd.EventName = ev.EventName
	}
	if d, ok := s.driverFromRequest(r); ok {
		pd.Authed = true
		pd.MyID = d.ID
		pd.IsAdmin = d.Role == "admin"
	}
	return pd
}

func (s *Server) render(w http.ResponseWriter, name string, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.Tmpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (s *Server) handleMonitorPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "monitor.html", s.pageData(r))
}

func (s *Server) handleRankingPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "ranking.html", s.pageData(r))
}

func (s *Server) handleRegisterPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "register.html", s.pageData(r))
}

func (s *Server) handleMyPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "mypage.html", s.pageData(r))
}

func (s *Server) handleAdminPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "admin.html", s.pageData(r))
}
