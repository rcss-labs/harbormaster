package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// OpsHandler is the whole HTTP surface of a poller or coordinator task: /healthz and /metrics, and a JSON 404 for
// everything else. It never mounts the API, the web app, or the fake IdP and notification sink.
func OpsHandler(ping func(context.Context) error, healthy func() error, metricsToken string, reg *telemetry.Registry, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		err := ping(r.Context())
		if err == nil && healthy != nil {
			err = healthy()
		}
		if err != nil {
			log.Error("healthz failed", "error", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("unavailable"))
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("GET /metrics", MetricsHandler(metricsToken, reg))
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	})
	return mux
}
