package web

import (
	"net/http"
	"strings"

	"timemon/internal/store"
)

// driverFromRequest resolves the tm_session cookie to a Driver. ok=false for
// any missing/invalid/unknown token - callers must not distinguish the
// reason (avoids leaking which case occurred).
func (s *Server) driverFromRequest(r *http.Request) (store.Driver, bool) {
	c, err := r.Cookie("tm_session")
	if err != nil || c.Value == "" {
		return store.Driver{}, false
	}
	d, ok, err := s.Store.GetDriverByToken(c.Value)
	if err != nil {
		return store.Driver{}, false
	}
	if !ok {
		return s.emergencyDriver(c.Value)
	}
	return d, true
}

// emergencyDriver returns the synthetic emergency-admin identity for tok.
// ok=false unless tok matches the emergency token generated at startup. The
// token stays valid for the whole process lifetime and rotates on every
// restart: it exists so an operator can always recover when nobody can log
// in as an admin anymore (all admin login URLs lost, admins dropped to zero
// by manual DB edits, ...). Anyone who can read the startup log is the
// server operator and could already edit the database directly, so the
// logged token grants no authority they do not have.
// The synthetic driver has no drivers row (ID 0): it exists to reach the
// admin user-management APIs and reissue tokens / promote a real admin, not
// to run an event (handlers that INSERT the acting driver's id into FK
// columns, e.g. queue.created_by, will fail for it). Restricted accordingly:
// withAdmin rejects it outright (403) on every route, and only the four
// user-management routes wrapped in withUserAdmin instead of withAdmin admit
// it - GET/POST /api/admin/users, POST /api/admin/users/{id}/reissue, and PUT
// /api/admin/users/{id}/role (see Routes()).
func (s *Server) emergencyDriver(tok string) (store.Driver, bool) {
	if tok == "" || s.emergencyToken == "" || tok != s.emergencyToken {
		return store.Driver{}, false
	}
	return store.Driver{ID: 0, Name: "emergency-admin", Role: "admin"}, true
}

// isEmergency reports whether d is the synthetic emergency-admin identity
// returned by emergencyDriver. It is the only identity with ID 0: real
// drivers rows are SQLite AUTOINCREMENT and start at 1.
func isEmergency(d store.Driver) bool {
	return d.ID == 0
}

// sessionCookieMaxAge makes tm_session a persistent cookie so drivers stay
// logged in across browser restarts. The backing token in drivers.token never
// expires, so the cookie lifetime is the only expiry in play; when it lapses,
// re-visiting the login URL / QR issues a fresh one.
const sessionCookieMaxAge = 365 * 24 * 3600

// setSessionCookie sets the tm_session auth cookie. Secure is only set when
// the request actually arrived over TLS (directly or via a trusted proxy
// header), so local/plain-HTTP LAN deployments keep working.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "tm_session",
		Value:    token,
		Path:     "/",
		MaxAge:   sessionCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
}

// clearSessionCookie expires the tm_session cookie immediately (MaxAge<0
// serializes as Max-Age=0, which browsers treat as "delete now"). Attributes
// other than Name/Path don't matter for deletion.
func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "tm_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// handleLogout implements POST /api/logout: drop the session cookie and
// return 204. No auth required - logging out an already-logged-out browser is
// a harmless no-op, and requiring auth would just make a half-broken cookie
// impossible to clear.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleTokenLogin implements GET /a/{token}: exchange a driver's permanent
// login token for a session cookie, then redirect to /mypage. Unknown tokens
// get a bodyless 404 (no distinction from "expired"/"revoked").
func (s *Server) handleTokenLogin(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	d, ok, err := s.Store.GetDriverByToken(token)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		// Not a known driver token - try the emergency-admin token before
		// giving up (see emergencyDriver for what it is and why it is safe).
		if _, ok := s.emergencyDriver(token); ok {
			s.setSessionCookie(w, r, token)
			// The emergency admin has no drivers row, so /mypage (the normal
			// post-login destination) would not work for it - send it
			// straight to the admin console instead.
			http.Redirect(w, r, "/admin", http.StatusFound)
			return
		}
		http.NotFound(w, r)
		return
	}
	s.setSessionCookie(w, r, d.Token)
	http.Redirect(w, r, "/mypage", http.StatusFound)
}

// withAuth requires a valid tm_session cookie and passes the resolved Driver
// through to the wrapped handler.
func (s *Server) withAuth(next func(w http.ResponseWriter, r *http.Request, d store.Driver)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.driverFromRequest(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next(w, r, d)
	}
}

// withAdmin requires a valid session belonging to an admin driver. It
// mirrors withAuth but additionally checks Role, following the same
// admin-check pattern already used for the SSE stream handler in Routes()
// ("d.Role == \"admin\"").
//
// The emergency-admin identity (see emergencyDriver) is rejected here too,
// even though its Role is "admin": its sole purpose is user management /
// token recovery, not running an event, so every admin route defaults to
// denying it (default-deny) unless the route is explicitly wrapped in
// withUserAdmin instead.
func (s *Server) withAdmin(next func(w http.ResponseWriter, r *http.Request, d store.Driver)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.driverFromRequest(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if d.Role != "admin" {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		if isEmergency(d) {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r, d)
	}
}

// withUserAdmin is withAdmin but additionally admits the emergency-admin
// identity. It exists only for the handful of routes that are the emergency
// admin's entire reason to exist - user management (list/create/reissue/role)
// - and must not be used for anything else; every other admin route should
// stay on withAdmin so it denies the emergency identity by default.
func (s *Server) withUserAdmin(next func(w http.ResponseWriter, r *http.Request, d store.Driver)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d, ok := s.driverFromRequest(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if d.Role != "admin" {
			writeJSONError(w, http.StatusForbidden, "forbidden")
			return
		}
		next(w, r, d)
	}
}

// withCSRFGuard rejects state-changing requests whose Sec-Fetch-Site header
// (when the browser sends one) indicates the request originated from
// another site. Same-origin requests send "same-origin" or omit the header
// entirely (older browsers) and are allowed through.
func (s *Server) withCSRFGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost, http.MethodPut, http.MethodDelete:
			if v := r.Header.Get("Sec-Fetch-Site"); v == "cross-site" || v == "same-site" {
				writeJSONError(w, http.StatusForbidden, "forbidden")
				return
			}
		}
		next(w, r)
	}
}

// withRateLimit applies a 10 req / 10 sec token bucket keyed by client IP.
func (s *Server) withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limiter.Allow(clientIP(r)) {
			writeJSONError(w, http.StatusTooManyRequests, "rate limited")
			return
		}
		next(w, r)
	}
}

// withCacheControl sets "Cache-Control: no-store" on every response by
// default. Deployments run behind Cloudflare (or any other shared/browser
// cache) now, and HTML pages plus JSON APIs carry per-session auth state
// (tm_session cookie) — caching them, even implicitly via a proxy default,
// risks leaking one user's data to another. This is applied as a single
// blanket wrapper around Routes() rather than sprinkled across every
// handler/helper so nothing new can slip through uncovered; the few
// responses that intentionally manage their own Cache-Control (icons,
// /static/, the SSE stream) are exempted below, and since they always set
// the header themselves afterwards, the exemption list is a belt-and-braces
// optimization rather than a correctness requirement.
func (s *Server) withCacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !cacheControlExempt(r.URL.Path) {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// cacheControlExempt reports whether path already manages its own
// Cache-Control header: /static/ (long-lived, versioned with the binary),
// the driver/vehicle icon endpoints (ETag-revalidated, no-cache), and the
// SSE stream (no-cache, set in internal/sse).
func cacheControlExempt(path string) bool {
	switch {
	case strings.HasPrefix(path, "/static/"):
		return true
	case path == "/api/stream":
		return true
	case strings.HasPrefix(path, "/api/drivers/") && strings.HasSuffix(path, "/icon"):
		return true
	case strings.HasPrefix(path, "/api/vehicles/") && strings.HasSuffix(path, "/icon"):
		return true
	default:
		return false
	}
}
