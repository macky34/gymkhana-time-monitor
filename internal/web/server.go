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

	qrcode "github.com/skip2/go-qrcode"

	embedded "timemon"
	"timemon/internal/snapshot"
	"timemon/internal/sse"
	"timemon/internal/store"
	"timemon/internal/timing"
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

	// emergencyToken is written once here in NewServer and never mutated
	// afterwards (unlike setupToken, which is cleared on successful setup
	// under setupMu), so it needs no mutex of its own.
	emergencyToken string
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

	// Stage-2 (multi-event): setup is a one-time, whole-database wizard, not
	// tied to "is there an active event right now" (an operator legitimately
	// has zero active events between event runs). Gate it on "has setup ever
	// completed" (IsSeeded) rather than "is there an admin right now"
	// (HasAdmin): admins can in principle drop to zero after setup has
	// completed (manual DB edits, a demotion race), and gating on HasAdmin
	// would let /setup run again in that case, which would re-run SeedEvent
	// against an already-seeded database.
	seeded, err := st.IsSeeded()
	if err != nil {
		return nil, err
	}
	if !seeded {
		tok, err := randToken()
		if err != nil {
			return nil, err
		}
		s.setupToken = tok
		logURLWithQR("Setup URL: ", baseURL+"/setup?t="+tok)
	} else {
		// Once setup has completed, mint a fresh emergency-admin token on
		// every start and log it. It authenticates as a synthetic admin for
		// the whole process lifetime (see emergencyDriver), giving the
		// operator a last-resort way back into /admin when nobody can log in
		// anymore (all admin login URLs lost, admins wiped by manual DB
		// edits, ...) without a restart. Log readers are server operators
		// with direct DB access anyway, so this leaks no new authority.
		tok, err := randToken()
		if err != nil {
			return nil, err
		}
		s.emergencyToken = tok
		logURLWithQR("Emergency admin URL (keep secret; rotates on every restart): ", baseURL+"/a/"+tok)
	}

	return s, nil
}

// logURLWithQR logs prefix+url followed by a terminal-rendered QR code of
// url, so an operator can scan it with a phone instead of typing a token URL
// off the console. Rendered for dark-background terminals (the common case
// for ssh/journalctl sessions). QR generation failure is non-fatal: the URL
// line itself is what matters.
func logURLWithQR(prefix, url string) {
	q, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		log.Printf("%s%s", prefix, url)
		return
	}
	log.Printf("%s%s\n%s", prefix, url, q.ToSmallString(false))
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

// Routes builds the full HTTP route table. The returned handler is the mux
// wrapped in withCacheControl, so every route gets "Cache-Control: no-store"
// by default (see withCacheControl for why that is done here, once, rather
// than in every handler).
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// ---- Pages (always 200, auth-optional; page JS handles the rest) ----
	for _, pr := range pageRoutes {
		mux.HandleFunc(pr.Pattern, s.pageHandler(pr.Template, pr.Active))
	}
	mux.HandleFunc("GET /my", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/mypage", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /setup", s.handleSetupPage)
	mux.Handle("GET /static/", s.staticHandler())
	mux.HandleFunc("GET /a/{token}", s.withRateLimit(s.handleTokenLogin))
	mux.HandleFunc("POST /api/logout", s.withRateLimit(s.withCSRFGuard(s.handleLogout)))

	// ---- Setup ----
	mux.HandleFunc("POST /api/setup", s.withCSRFGuard(s.handleAPISetup))

	// ---- Realtime stream ----
	mux.Handle("GET /api/stream", s.Hub.Handler(func(r *http.Request) bool {
		d, ok := s.driverFromRequest(r)
		return ok && d.Role == "admin" && !isEmergency(d)
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
	mux.HandleFunc("GET /api/vehicles/{id}/icon", s.handleVehicleIcon)

	// ---- Archive (public, closed events only) ----
	mux.HandleFunc("GET /api/archive/events", s.handleAPIArchiveEvents)
	mux.HandleFunc("GET /api/archive/{id}/ranking", s.handleAPIArchiveRanking)
	mux.HandleFunc("GET /api/archive/{id}/recent", s.handleAPIArchiveRecent)

	// ---- Registration ----
	mux.HandleFunc("POST /api/register", s.withRateLimit(s.withCSRFGuard(s.handleRegister)))

	// ---- Mypage (authenticated participant self-service) ----
	mux.HandleFunc("GET /api/mypage", s.withRateLimit(s.withAuth(s.handleGetMy)))
	mux.HandleFunc("PUT /api/mypage/profile", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleUpdateProfile))))
	mux.HandleFunc("POST /api/mypage/icon", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyIcon))))
	mux.HandleFunc("GET /api/mypage/qr", s.withRateLimit(s.withAuth(s.handleMyQR)))
	mux.HandleFunc("POST /api/mypage/vehicles", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleAddMyVehicle))))
	mux.HandleFunc("DELETE /api/mypage/vehicles/{id}", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleDeleteMyVehicle))))
	mux.HandleFunc("POST /api/mypage/vehicles/{id}/icon", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyVehicleIcon))))
	mux.HandleFunc("PUT /api/mypage/vehicles/{id}", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleUpdateMyVehicle))))
	mux.HandleFunc("PUT /api/mypage/main-vehicle", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleSetMainVehicle))))
	mux.HandleFunc("POST /api/mypage/queue", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyQueueAdd))))
	mux.HandleFunc("DELETE /api/mypage/queue", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyQueueCancel))))
	mux.HandleFunc("POST /api/mypage/queue/launch", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyLaunch))))
	mux.HandleFunc("DELETE /api/mypage/queue/launch", s.withRateLimit(s.withCSRFGuard(s.withAuth(s.handleMyLaunchUndo))))

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
	// ---- Admin: user management (W4) ----
	// The first four routes are also the emergency-admin's entire reason to
	// exist (see emergencyDriver / withUserAdmin), so they alone use
	// withUserAdmin; rename and icon stay on withAdmin (emergency-denied).
	mux.HandleFunc("GET /api/admin/users", s.withUserAdmin(s.handleAdminUsersList))
	mux.HandleFunc("POST /api/admin/users", s.withCSRFGuard(s.withUserAdmin(s.handleAdminUserCreate)))
	mux.HandleFunc("PUT /api/admin/users/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminUserUpdate)))
	mux.HandleFunc("POST /api/admin/users/{id}/reissue", s.withCSRFGuard(s.withUserAdmin(s.handleAdminUserReissue)))
	mux.HandleFunc("PUT /api/admin/users/{id}/role", s.withCSRFGuard(s.withUserAdmin(s.handleAdminUserRole)))
	mux.HandleFunc("POST /api/admin/users/{id}/icon", s.withCSRFGuard(s.withAdmin(s.handleAdminUserIcon)))
	// ---- Admin: vehicle management (W4) ----
	mux.HandleFunc("POST /api/admin/vehicles", s.withCSRFGuard(s.withAdmin(s.handleAdminVehicleCreate)))
	mux.HandleFunc("PUT /api/admin/vehicles/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminVehicleUpdate)))
	mux.HandleFunc("DELETE /api/admin/vehicles/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminVehicleDelete)))
	mux.HandleFunc("POST /api/admin/vehicles/{id}/icon", s.withCSRFGuard(s.withAdmin(s.handleAdminVehicleIcon)))
	// ---- Admin: settings (W4) ----
	mux.HandleFunc("GET /api/admin/settings", s.withAdmin(s.handleAdminSettingsGet))
	mux.HandleFunc("PUT /api/admin/settings", s.withCSRFGuard(s.withAdmin(s.handleAdminSettingsUpdate)))
	mux.HandleFunc("PUT /api/admin/registration", s.withCSRFGuard(s.withAdmin(s.handleAdminRegistrationSet)))
	// ---- Admin: event management (stage 2 multi-event) ----
	mux.HandleFunc("GET /api/admin/events", s.withAdmin(s.handleAdminEventsList))
	mux.HandleFunc("POST /api/admin/events", s.withCSRFGuard(s.withAdmin(s.handleAdminEventCreate)))
	mux.HandleFunc("POST /api/admin/events/{id}/close", s.withCSRFGuard(s.withAdmin(s.handleAdminEventClose)))
	// ---- Admin: log management (W4) ----
	mux.HandleFunc("GET /api/admin/logs", s.withAdmin(s.handleAdminLogsList))
	mux.HandleFunc("POST /api/admin/logs", s.withCSRFGuard(s.withAdmin(s.handleAdminLogCreate)))
	mux.HandleFunc("PUT /api/admin/logs/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminLogUpdate)))
	mux.HandleFunc("DELETE /api/admin/logs/{id}", s.withCSRFGuard(s.withAdmin(s.handleAdminLogDelete)))
	mux.HandleFunc("PUT /api/admin/logs/{id}/assign", s.withCSRFGuard(s.withAdmin(s.handleAdminLogAssign)))
	// ---- Admin: CSV export (W4) ----
	mux.HandleFunc("GET /api/admin/export", s.withAdmin(s.handleAdminExport))
	// ---- Admin: sensors (W4) ----
	mux.HandleFunc("GET /api/admin/sensors", s.withAdmin(s.handleAdminSensors))

	// ---- Internal (LAN only): ESP32 fetches its debounce lockout at boot ----
	mux.Handle("GET /api/internal/sensor-config", timing.SensorConfigHandler(func() int {
		ev, ok, err := s.Store.GetActiveEvent()
		if err != nil || !ok {
			return 800 // defaults.json fallback
		}
		return ev.SensorLockoutMS
	}))

	return s.withCacheControl(mux)
}
