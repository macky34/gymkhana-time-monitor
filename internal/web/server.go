// Package web implements the HTTP surface of timemon: pages, public JSON
// snapshot APIs, participant self-service APIs, and the realtime SSE stream.
//
// NOTE: this file is the only one in the package that imports
// timemon/internal/sse and timemon/internal/snapshot. Every other file in
// the package compiles on its own even if those two packages do not exist
// yet (they are being built in parallel by another work stream).
package web

import (
	"crypto/rand"
	"encoding/base64"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"sync"
	"time"

	embedded "timemon"
	"timemon/internal/snapshot"
	"timemon/internal/sse"
	"timemon/internal/store"
)

// Server holds all shared dependencies for the web layer.
type Server struct {
	Store   *store.Store
	Hub     *sse.Hub
	Snap    *snapshot.Builder
	Tmpl    *template.Template
	BaseURL string

	limiter *rateLimiter
	course  *courseManager
	orphans orphanTracker

	setupMu    sync.Mutex
	setupToken string
}

// NewServer builds a Server. If the event has not been configured yet, a
// one-time setup token is generated and logged so an operator can open the
// /setup page.
func NewServer(st *store.Store, hub *sse.Hub, snap *snapshot.Builder, baseURL string) (*Server, error) {
	tmplFS := embedded.Templates()

	matches, err := fs.Glob(tmplFS, "*.html")
	if err != nil {
		return nil, err
	}

	var tmpl *template.Template
	if len(matches) == 0 {
		tmpl = template.New("empty")
	} else {
		tmpl, err = template.ParseFS(tmplFS, "*.html")
		if err != nil {
			return nil, err
		}
	}

	s := &Server{
		Store:   st,
		Hub:     hub,
		Snap:    snap,
		Tmpl:    tmpl,
		BaseURL: baseURL,
		limiter: newRateLimiter(10, 10*time.Second),
	}
	s.course = newCourseManager(s)
	snap.SetFinishProvider(s.course.finishProvider)

	_, ok, err := st.GetSettings()
	if err != nil {
		return nil, err
	}
	if !ok {
		tok, err := randToken()
		if err != nil {
			return nil, err
		}
		s.setupToken = tok
		log.Printf("Setup URL: %s/setup?t=%s", baseURL, tok)
	}

	return s, nil
}

// randToken generates a URL-safe random token (driver login tokens, setup
// token). 24 raw bytes -> 32 base64url characters, no padding.
func randToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Routes builds the full HTTP route table.
func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()

	// ---- Pages (always 200, auth-optional; page JS handles the rest) ----
	mux.HandleFunc("GET /{$}", s.handleMonitorPage)
	mux.HandleFunc("GET /ranking", s.handleRankingPage)
	mux.HandleFunc("GET /register", s.handleRegisterPage)
	mux.HandleFunc("GET /my", s.handleMyPage)
	mux.HandleFunc("GET /admin", s.handleAdminPage)
	mux.HandleFunc("GET /setup", s.handleSetupPage)
	mux.Handle("GET /static/", s.staticHandler())
	mux.HandleFunc("GET /a/{token}", s.withRateLimit(s.handleTokenLogin))

	// ---- Setup ----
	mux.HandleFunc("POST /api/setup", s.withCSRFGuard(s.handleAPISetup))

	// ---- Realtime stream ----
	mux.Handle("GET /api/stream", s.Hub.Handler(func(r *http.Request) bool {
		d, ok := s.driverFromRequest(r)
		return ok && d.Role == "admin"
	}))

	// ---- Public snapshot / read APIs ----
	mux.HandleFunc("GET /api/ranking", s.handleAPIRanking)
	mux.HandleFunc("GET /api/queue", s.handleAPIQueue)
	mux.HandleFunc("GET /api/settings", s.handleAPISettings)
	mux.HandleFunc("GET /api/recent", s.handleAPIRecent)
	mux.HandleFunc("GET /api/combinations/{d}/{v}/logs", s.handleAPICombinationLogs)
	mux.HandleFunc("GET /api/drivers", s.handleAPIDrivers)
	mux.HandleFunc("GET /api/vehicles", s.handleAPIVehicles)
	mux.HandleFunc("GET /api/drivers/{id}/icon", s.handleDriverIcon)

	// ---- Registration ----
	mux.HandleFunc("POST /api/register", s.withRateLimit(s.withCSRFGuard(s.handleRegister)))

	// ---- My (authenticated participant self-service) ----
	mux.HandleFunc("GET /api/my", s.withRateLimit(s.withAuth(s.handleGetMy)))
	mux.HandleFunc("PUT /api/my/profile", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleUpdateProfile))))
	mux.HandleFunc("POST /api/my/icon", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyIcon))))
	mux.HandleFunc("GET /api/my/qr", s.withRateLimit(s.withAuth(s.handleMyQR)))
	mux.HandleFunc("POST /api/my/vehicles", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleAddMyVehicle))))
	mux.HandleFunc("DELETE /api/my/vehicles/{id}", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleDeleteMyVehicle))))
	mux.HandleFunc("PUT /api/my/main-vehicle", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleSetMainVehicle))))
	mux.HandleFunc("POST /api/my/queue", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyQueueAdd))))
	mux.HandleFunc("DELETE /api/my/queue", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyQueueCancel))))
	mux.HandleFunc("POST /api/my/queue/launch", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyLaunch))))
	mux.HandleFunc("DELETE /api/my/queue/launch", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyLaunchUndo))))

	// ---- Admin: course control (W3) ----
	mux.HandleFunc("POST /api/admin/course", s.withCSRFGuard(s.withAdmin(s.handleAdminCourseLaunch)))
	mux.HandleFunc("POST /api/admin/course/finish", s.withCSRFGuard(s.withAdmin(s.handleAdminCourseFinishOldest)))
	mux.HandleFunc("POST /api/admin/course/{id}/finish", s.withCSRFGuard(s.withAdmin(s.handleAdminCourseFinishByID)))
	mux.HandleFunc("DELETE /api/admin/course/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminCourseCancel)))
	mux.HandleFunc("POST /api/admin/course/{id}/undo-start", s.withCSRFGuard(s.withAdmin(s.handleAdminCourseUndoStart)))
	mux.HandleFunc("POST /api/admin/course/{id}/undo-goal", s.withCSRFGuard(s.withAdmin(s.handleAdminCourseUndoGoal)))
	mux.HandleFunc("PUT /api/admin/course/{id}/pt", s.withCSRFGuard(s.withAdmin(s.handleAdminCoursePT)))
	mux.HandleFunc("PUT /api/admin/course/{id}/mc", s.withCSRFGuard(s.withAdmin(s.handleAdminCourseMC)))

	// ---- Admin: queue management (W3) ----
	mux.HandleFunc("POST /api/admin/queue", s.withCSRFGuard(s.withAdmin(s.handleAdminQueueAdd)))
	mux.HandleFunc("PUT /api/admin/queue/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminQueueReorder)))
	mux.HandleFunc("DELETE /api/admin/queue/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminQueueCancel)))
	// TODO(W4): admin management endpoints - /api/admin/drivers, /api/admin/vehicles, /api/admin/settings, /api/admin/logs, etc.

	return mux
}
