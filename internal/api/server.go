// Package api is Harbormaster's HTTP layer: middleware, error mapping and handlers.
package api

import (
	"cmp"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/execbridge"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
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
	// Metrics reads CloudWatch metrics (plan 03); New defaults it from Clients, Store and Cfg.
	Metrics *metrics.Client
	// Logs reads CloudWatch Logs history and live tails (plan 03); New defaults it from Clients.
	Logs *logs.Client
	// TailLimiter caps concurrent live tails per user; New defaults it from Cfg.LogTailsPerUser.
	TailLimiter *logs.TailLimiter
	// Deploy runs the change path (plan 02).
	Deploy *deploy.Service

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

	// plan 05: reads EFS metrics for GET /api/storage; New defaults it to Metrics. Tests inject a failing one.
	efsFetcher efsMetrics
	// plan 06
	// MetricsToken is the bearer token for GET /metrics; empty disables the endpoint.
	MetricsToken string
	// Telemetry is the registry /metrics renders; New defaults it to telemetry.Default.
	Telemetry *telemetry.Registry

	// plan 07: SAML single sign-on. New fills SAML from the other fields; tests may pre-set parts of it.
	SAML *samlService
	// FakeIdP is the dev-only fake identity provider, mounted at /fake-idp/ when non-nil.
	FakeIdP http.Handler
}

// New returns s ready to serve; it fills in defaults for Log and Now.
func New(s Server) *Server {
	if s.Log == nil {
		s.Log = slog.Default()
	}
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.Metrics == nil {
		s.Metrics = metrics.New(s.Clients, s.Store, s.Cfg.MetricsCacheTTL)
		s.Metrics.Now, s.Metrics.Log = s.Now, s.Log
	}
	if s.efsFetcher == nil {
		s.efsFetcher = s.Metrics // plan 05
	}
	if s.Logs == nil {
		s.Logs = &logs.Client{Clients: s.Clients, Now: s.Now, MaxLookback: 7 * 24 * time.Hour}
	}
	if s.TailLimiter == nil {
		s.TailLimiter = logs.NewTailLimiter(cmp.Or(s.Cfg.LogTailsPerUser, defaultTailsPerUser))
	}
	// plan 06
	if s.Telemetry == nil {
		s.Telemetry = telemetry.Default
	}
	// plan 07
	s.SAML = newSAMLService(s.SAML, &s)
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
	h = SecurityHeaders(h)
	h = s.requestLog(h)
	return s.recoverMiddleware(h)
}

// onlyAPI applies mw to /api paths so static files and /healthz never touch the session store.
// The SAML ACS is excluded: it must not read or touch the session cookie (it is CSRF-exempt because
// it acts on nothing but the request row and the binding cookie).
func onlyAPI(mw func(http.Handler) http.Handler, next http.Handler) http.Handler {
	wrapped := mw(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == samlACSPath {
			next.ServeHTTP(w, r)
			return
		}
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
	// plan 06: token-protected, deliberately outside session auth, CSRF and ETag.
	mux.Handle("GET /metrics", MetricsHandler(s.MetricsToken, s.Telemetry))

	s.handle(mux, "POST /api/auth/login", s.login, routeOptions{Public: true})
	s.handle(mux, "GET /api/auth/setup", s.setupNeeded, routeOptions{Public: true})
	s.handle(mux, "POST /api/auth/setup", s.setupComplete, routeOptions{Public: true})
	// plan 07: SAML single sign-on. The ACS is the only new CSRF-exempt POST; it never reads the session.
	s.handle(mux, "GET /api/auth/options", s.authOptions, routeOptions{Public: true})
	s.handle(mux, "GET /api/auth/saml/login", s.samlLogin, routeOptions{Public: true, NoETag: true})
	s.handle(mux, "POST "+samlACSPath, s.samlACS, routeOptions{Public: true, NoETag: true})
	s.handle(mux, "GET /api/auth/saml/metadata", s.samlMetadata, routeOptions{Public: true})
	s.handle(mux, "POST /api/auth/saml/link", s.samlLinkStart, routeOptions{})
	// The catch-all below would answer a GET with 404; both are POST-only.
	postOnly := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Use POST.")
	}
	s.handle(mux, "GET /api/auth/saml/link", postOnly, routeOptions{Public: true, NoETag: true})
	s.handle(mux, "GET "+samlACSPath, postOnly, routeOptions{Public: true, NoETag: true})
	if s.FakeIdP != nil {
		mux.Handle("/fake-idp/", s.FakeIdP)
	}
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
	// plan 07: SSO administration
	s.handle(mux, "POST /api/users/{id}/break-glass", s.setBreakGlass, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/users/{id}/sso-link", s.setPendingLink, routeOptions{Admin: true})
	s.handle(mux, "DELETE /api/users/{id}/sso-link", s.clearPendingLink, routeOptions{Admin: true})
	s.handle(mux, "POST /api/users/{id}/sso-unlink", s.unlinkSSO, routeOptions{Admin: true})
	s.handle(mux, "GET /api/sso", s.getSSO, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/sso", s.putSSO, routeOptions{Admin: true})
	s.handle(mux, "POST /api/sso/metadata", s.importSSOMetadata, routeOptions{Admin: true})
	s.handle(mux, "PUT /api/sso/enabled", s.setSSOEnabled, routeOptions{Admin: true})
	s.handle(mux, "POST /api/sso/key", s.rotateSSOKey, routeOptions{Admin: true})
	s.handle(mux, "GET /api/sso/tests/{id}", s.takeSSOTest, routeOptions{Admin: true, NoETag: true})
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
	s.handle(mux, "GET /api/settings", s.getSettings, routeOptions{})
	s.handle(mux, "PUT /api/settings", s.putSettings, routeOptions{Admin: true})
	s.observeRoutes(mux)
	s.networkRoutes(mux) // plan 05
	s.storageRoutes(mux) // plan 05

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
