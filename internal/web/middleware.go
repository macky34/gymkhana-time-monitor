package web

import (
	"net/http"

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
// login token for a session cookie, then redirect to /my. Unknown tokens get
// a bodyless 404 (no distinction from "expired"/"revoked").
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
	http.Redirect(w, r, "/my", http.StatusFound)
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
