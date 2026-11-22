package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// MetricsHandler serves the Prometheus text exposition behind a static bearer token.
// An empty token disables the endpoint (404). It sits outside session auth, CSRF and ETag.
func MetricsHandler(token string, reg *telemetry.Registry) http.Handler {
	return metricsHandler(token, reg, nil)
}

// metricsHandler is MetricsHandler with an optional extra section appended after the registry's series. extra reads
// from the database at scrape time; it returns the text to append, or "" to leave its series out.
func metricsHandler(token string, reg *telemetry.Registry, extra func(context.Context) string) http.Handler {
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
		if extra != nil {
			ctx, cancel := context.WithTimeout(r.Context(), metricsScrapeBound)
			defer cancel()
			_, _ = w.Write([]byte(extra(ctx)))
		}
	})
}

// metricsScrapeBound caps the database read behind the member and lease series, so a slow database never holds up a scrape.
const metricsScrapeBound = 2 * time.Second
