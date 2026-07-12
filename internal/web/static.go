package web

import (
	"net/http"

	embedded "timemon"
)

// staticHandler serves embedded.Static() under /static/ with a long-lived
// cache header (the embedded assets are versioned with the binary).
func (s *Server) staticHandler() http.Handler {
	fileServer := http.FileServerFS(embedded.Static())
	return http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=604800")
		fileServer.ServeHTTP(w, r)
	}))
}
