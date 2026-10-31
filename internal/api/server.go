// Package api is Harbormaster's HTTP layer: middleware, error mapping and handlers.
package api

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/web"
)

// Server holds the dependencies of every handler. Later plans add fields as they add handlers.
type Server struct {
	Store   *store.Store
	Auth    *auth.Service
	Setup   *auth.Setup
	Clients awsx.Clients
	Prober  awsx.Prober
	Cfg     config.Config
	Log     *slog.Logger
	Now     func() time.Time
}

// New returns s ready to serve; it fills in defaults for Log and Now.
func New(s Server) *Server {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	return &s
}

func (s *Server) log() *slog.Logger { return s.Log }

func (s *Server) now() time.Time { return s.Now() }

// Handler builds the mux and wraps it:
// recover -> request log -> security headers -> auth.Middleware (for /api).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	var h http.Handler = mux
	h = onlyAPI(s.Auth.Middleware, h)
	h = withTagsMemo(h)
	h = securityHeaders(h)
	h = s.requestLog(h)
	return s.recoverMiddleware(h)
}

// onlyAPI applies mw to /api paths so static files and /healthz never touch the session store.
func onlyAPI(mw func(http.Handler) http.Handler, next http.Handler) http.Handler {
	wrapped := mw(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			wrapped.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handle registers pattern with the per-route middleware chain from opts.
func (s *Server) handle(mux *http.ServeMux, pattern string, h http.HandlerFunc, opts routeOptions) {
	mux.Handle(pattern, protect(h, opts))
}

// routes registers every route. Plans 02-05 append their handlers here.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)

	s.handle(mux, "POST /api/auth/login", s.login, routeOptions{Public: true})
	s.handle(mux, "GET /api/auth/setup", s.setupNeeded, routeOptions{Public: true})
	s.handle(mux, "POST /api/auth/setup", s.setupComplete, routeOptions{Public: true})
	s.handle(mux, "POST /api/auth/logout", s.logout, routeOptions{})
	s.handle(mux, "POST /api/auth/password", s.changePassword, routeOptions{})
	s.handle(mux, "GET /api/me", s.me, routeOptions{})
	s.handle(mux, "GET /api/overview", s.overview, routeOptions{})
	s.handle(mux, "GET /api/clusters", s.listClusters, routeOptions{})
	s.handle(mux, "GET /api/clusters/{clusterId}", s.getCluster, routeOptions{})

	// Unknown API paths are JSON 404s and never fall through to the SPA. RequirePasswordChanged
	// still applies so a user with a pending password change sees password_change_required.
	mux.Handle("/api/", auth.RequirePasswordChanged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	})))

	mux.Handle("/", web.Handler())
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := s.Store.Ping(r.Context()); err != nil {
		s.log().Error("healthz ping failed", "error", err)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("unavailable"))
		return
	}
	_, _ = w.Write([]byte("ok"))
}
