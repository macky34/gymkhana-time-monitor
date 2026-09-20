package timing

import (
	"fmt"
	"net/http"
)

// SensorConfigHandler returns an http.Handler serving the configuration the
// ESP32 sensors fetch at boot:
//
//	GET -> {"lockout_sec":10}
//
// lockoutSec is invoked on every request so the response always reflects the
// current setting. Restricting which source IPs may reach this endpoint is
// the caller's responsibility (main.go / reverse proxy), not this handler's.
func SensorConfigHandler(lockoutSec func() float64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"lockout_sec":%g}`, lockoutSec())
	})
}
