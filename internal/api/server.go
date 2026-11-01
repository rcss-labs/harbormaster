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
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/execbridge"
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
	Deploy  *deploy.Service

	// plan 04: terminal
	// ExecLimiter caps concurrent terminal sessions per user; New creates it from Cfg when nil.
	ExecLimiter *execbridge.Limiter
	// ExecStartTimeout is how long a new terminal socket may take to send its start frame (default 5s).
	ExecStartTimeout time.Duration
	// ExecPingInterval and ExecPingTimeout drive the keepalive ping on the browser socket
	// (defaults 30s and 15s); a peer that misses one is treated as gone.
	ExecPingInterval time.Duration
	ExecPingTimeout  time.Duration
	// ExecAllowInsecureStream permits ws:// SSM streams to loopback; only --fake-aws and tests set it.
	ExecAllowInsecureStream bool
}

// New returns s ready to serve; it fills in defaults for Log and Now.
func New(s Server) *Server {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	// plan 04: terminal
	if s.ExecLimiter == nil {
		s.ExecLimiter = execbridge.NewLimiter(s.Cfg.ExecMaxSessionsPerUser)
	}
	if s.ExecStartTimeout <= 0 {
		s.ExecStartTimeout = 5 * time.Second
	}
	if s.ExecPingInterval <= 0 {
		s.ExecPingInterval = 30 * time.Second
	}
	if s.ExecPingTimeout <= 0 {
		s.ExecPingTimeout = 15 * time.Second
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
	s.handle(mux, "GET /api/services/{serviceId}", s.getService, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/tasks", s.listServiceTasks, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/operations", s.serviceOperations, routeOptions{})
	s.handle(mux, "GET /api/tasks", s.listTasks, routeOptions{})
	s.handle(mux, "GET /api/search", s.search, routeOptions{})
	s.handle(mux, "GET /api/poller", s.pollerStatus, routeOptions{})
	// plan 04: terminal
	s.handle(mux, "GET /api/tasks/{taskId}/exec-targets", s.execTargets, routeOptions{})
	s.handle(mux, "GET /api/tasks/{taskId}/exec", s.execWS, routeOptions{NoETag: true})

	s.handle(mux, "GET /api/users", s.listUsers, routeOptions{Admin: true})
	s.handle(mux, "POST /api/users", s.createUser, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/users/{id}", s.deleteUser, routeOptions{Admin: true})
	s.handle(mux, "POST /api/users/{id}/reset-password", s.resetUserPassword, routeOptions{Admin: true})
	s.handle(mux, "POST /api/users/{id}/admin", s.setUserAdmin, routeOptions{Admin: true})
	s.handle(mux, "GET /api/permissions", s.getPermissions, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/permissions/{userId}/{tagId}", s.setPermission, routeOptions{Admin: true})
	s.handle(mux, "GET /api/tags", s.listTagItems, routeOptions{Admin: true})
	s.handle(mux, "POST /api/tags", s.createTag, routeOptions{Admin: true})
	s.handle(mux, "PATCH /api/tags/{id}", s.updateTag, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/tags/{id}", s.deleteTag, routeOptions{Admin: true})
	s.handle(mux, "GET /api/tags/{id}/impact", s.tagImpact, routeOptions{Admin: true})
	s.handle(mux, "GET /api/tag-assignments", s.listTagAssignments, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/tag-assignments/clusters/{clusterId}", s.assignClusterTag, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/tag-assignments/services/{serviceId}", s.assignServiceTag, routeOptions{Admin: true})
	s.handle(mux, "GET /api/accounts", s.listAccounts, routeOptions{Admin: true})
	s.handle(mux, "GET /api/accounts/external-id", s.getExternalID, routeOptions{Admin: true})
	s.handle(mux, "POST /api/accounts/test", s.testAccounts, routeOptions{Admin: true})
	s.handle(mux, "POST /api/accounts", s.createAccount, routeOptions{Admin: true})
	s.handle(mux, "PATCH /api/accounts/{id}", s.updateAccount, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/accounts/{id}", s.deleteAccount, routeOptions{Admin: true})
	s.handle(mux, "GET /api/audit", s.listAudit, routeOptions{Admin: true})

	// Change-path reads (plan 02).
	s.handle(mux, "GET /api/services/{serviceId}/task-definition", s.getTaskDefinition, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/revisions", s.getRevisions, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/image-tags", s.getImageTags, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/desired-count", s.getDesiredCount, routeOptions{})

	// Change-path writes (plan 02).
	s.handle(mux, "POST /api/services/{serviceId}/revisions", s.registerRevision, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/deploy", s.deployRevision, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/restart", s.restartService, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/desired-count", s.setDesiredCount, routeOptions{})
	s.handle(mux, "POST /api/tasks/{taskId}/stop", s.stopTask, routeOptions{})

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
