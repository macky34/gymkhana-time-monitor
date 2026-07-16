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
	if err != nil || !ok {
		return store.Driver{}, false
	}
	return d, true
}

// setSessionCookie sets the tm_session auth cookie. Secure is only set when
// the request actually arrived over TLS (directly or via a trusted proxy
// header), so local/plain-HTTP LAN deployments keep working.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{
		Name:     "tm_session",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
	})
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
