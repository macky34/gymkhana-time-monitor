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

// pageRoute is one entry of pageRoutes: a mux pattern paired with the
// template it renders.
type pageRoute struct {
	Pattern  string
	Template string
}

// pageRoutes lists every server-rendered page whose handler is nothing more
// than render(w, template, pageData(r)) - all per-page dynamic behavior
// comes from client-side JS hitting the JSON APIs / SSE stream instead.
// Registered in Routes() via pageHandler. Pages with extra logic (redirects,
// token checks, ...) are registered directly instead.
var pageRoutes = []pageRoute{
	{"GET /{$}", "monitor.html"},
	{"GET /ranking", "ranking.html"},
	{"GET /register", "register.html"},
	{"GET /mypage", "mypage.html"},
	{"GET /admin", "admin.html"},
	{"GET /archive", "archive.html"},
}

// pageHandler builds the http.HandlerFunc for one pageRoutes entry.
func (s *Server) pageHandler(template string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.render(w, template, s.pageData(r))
	}
}
