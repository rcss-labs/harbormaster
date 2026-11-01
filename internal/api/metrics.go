package api

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// MetricsHandler serves the Prometheus text exposition behind a static bearer token.
// An empty token disables the endpoint (404). It sits outside session auth, CSRF and ETag.
func MetricsHandler(token string, reg *telemetry.Registry) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token == "" {
			writeError(w, http.StatusNotFound, "not_found", "Not found")
			return
		}
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			writeError(w, http.StatusUnauthorized, "unauthenticated", "A valid metrics token is required")
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = reg.WritePrometheus(w)
	})
}
