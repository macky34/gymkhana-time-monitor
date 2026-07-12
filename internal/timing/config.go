package timing

import (
	"fmt"
	"net/http"
)

// SensorConfigHandler returns an http.Handler serving the configuration the
// ESP32 sensors fetch at boot:
//
//	GET -> {"lockout_ms":800}
//
// lockoutMS is invoked on every request so the response always reflects the
// current setting. Restricting which source IPs may reach this endpoint is
// the caller's responsibility (main.go / reverse proxy), not this handler's.
func SensorConfigHandler(lockoutMS func() int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"lockout_ms":%d}`, lockoutMS())
	})
}
