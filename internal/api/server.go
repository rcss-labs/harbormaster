// Package api is Harbormaster's HTTP layer: middleware, error mapping and handlers.
package api

import (
	"context"
	"github.com/rcss-labs/harbormaster/internal/cost"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agentclient"
	"github.com/rcss-labs/harbormaster/internal/auth"
	"github.com/rcss-labs/harbormaster/internal/awsx"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/deploy"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/kv"
	"github.com/rcss-labs/harbormaster/internal/limits"
	"github.com/rcss-labs/harbormaster/internal/logs"
	"github.com/rcss-labs/harbormaster/internal/metrics"
	"github.com/rcss-labs/harbormaster/internal/notify"
	"github.com/rcss-labs/harbormaster/internal/oauthsrv"
	"github.com/rcss-labs/harbormaster/internal/offhours"
	"github.com/rcss-labs/harbormaster/internal/safedial"
	"github.com/rcss-labs/harbormaster/internal/secretbox"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
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
	// LogErrors counts error lines per bucket for the service timeline; New builds the one counter every
	// request shares, since the CloudWatch pacing only holds process-wide through a single counter.
	LogErrors *logs.ErrorCounter
	// Limits counts the per-user terminal sessions ("exec"), live tails ("tail") and running log searches ("search");
	// New defaults it to a per-task counter, and serve wires the estate-wide one when a store is configured.
	Limits limits.Concurrency
	// Rates holds the token buckets behind log search, the notification test button and the SAML login start
	// ("logsearch", "notifytest", "samllogin"); New defaults it to per-task buckets on Now.
	Rates limits.Rate
	// LogSearch runs Logs Insights searches across services; New defaults it from Clients and Cfg.
	LogSearch *logs.Searcher
	// draining is closed by BeginDrain when the process starts shutting down; running log searches and agent runs watch it
	// so they stop their Insights queries and audit inside the shutdown drain.
	draining      chan struct{}
	beginDrainOne *sync.Once
	// Deploy runs the change path (plan 02).
	Deploy *deploy.Service

	// plan 04: terminal
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

	// Scan wakes the scan loop after a scan request; nil when scanning is off.
	Scan ScanKicker
	// OffHours applies manual wake and sleep of off-hours services; nil when off-hours is not wired.
	OffHours *offhours.Applier
	// OffHoursTimeout bounds the lock wait and the AWS work of a manual wake or sleep (default 20 s).
	OffHoursTimeout time.Duration
	// KickOffHours wakes the off-hours reconciler after a schedule change; nil is a no-op. serve wires it to the signal
	// bus, which reaches the active coordinator; if nobody hears it a change waits at most HM_OFFHOURS_INTERVAL.
	KickOffHours func()
	// FakeNotify is the in-process notification receiver, mounted at /fake-notify/ when non-nil; only fake AWS mode sets it.
	FakeNotify http.Handler
	// Notify sends the test message and validates destinations; New builds it from Cfg when nil.
	Notify *notify.Senders
	// NotifyBox seals and opens route secrets; New builds it from Cfg.SessionKey when nil, and it stays nil without a key.
	NotifyBox *secretbox.Box
	// KickNotify wakes the notification dispatcher after a route change; nil is a no-op. Like KickOffHours it travels
	// on the signal bus; if nobody hears it a change waits at most HM_NOTIFY_INTERVAL.
	KickNotify func()
	// KVHealth reports whether the shared Valkey or Redis store is reachable, for the poller status; nil means there is
	// no store.
	KVHealth kv.Upper
	// Focus is the set of pages open in a visible tab, and Bus carries the poll-now signal to the poller that owns the
	// slice. A nil Focus turns live refresh off: the focus endpoint answers "unavailable".
	Focus focus.Registry
	Bus   signal.Bus
	// Snap serves the cluster and service pages from versioned read models in the shared store (internal/snapcache); nil
	// means the cache is off and the handlers read Postgres as they always did.
	Snap *snapcache.Reader
	// costWarned remembers which unreadable container definitions the cost tab already logged.
	costWarned *warnedSet
	// rateForClass prices a task. It is cost.RateForClass unless a test swaps in a table without some price.
	rateForClass func(region, architecture string, class cost.TaskClass) (cost.Rate, bool)
	// focusOpens remembers when each object was last opened through this task, for the wait for its first live read.
	focusOpens *focusOpens
	// targetKeys and memberLines hold short-lived copies of reads that every list request or metrics scrape repeats.
	targetKeys  *ttlCache[[]store.TargetFilter]
	memberLines *ttlCache[string]
	// routeLog records the routes Routes asks for; New creates it.
	routeLog *routeRecorder
	// OAuth is the authorization server for MCP connectors; New builds it when HM_OAUTH is on, and its routes exist
	// only then.
	OAuth *oauthsrv.Service
	// Agent is the client for the agent service; nil while HM_AGENT_URL is unset. The chat routes exist only when
	// Cfg.AgentEnabled().
	Agent *agentclient.Client
	// AgentHeartbeat is how often an open chat stream gets a comment line to keep proxies from closing it
	// (default 15s).
	AgentHeartbeat time.Duration
	// AgentWriteTimeout bounds one small write of a chat stream to the browser (default 10s); a browser that reads
	// nothing for that long is dropped and its run cancelled.
	AgentWriteTimeout time.Duration
	// AgentRunSlack is how long past the agent's own run timeout a turn waits before giving up (default 30s).
	AgentRunSlack time.Duration
	// agentReach remembers for a few seconds that the agent's catalogue could not be read, so a page full of status
	// calls does not queue on a dead agent.
	agentReach *agentReach
	// agentEventHook, when set, sees each agent event before the turn handles it. TEST ONLY: it lets a test hold
	// the event loop at a known point.
	agentEventHook func(ctx context.Context, ev agentclient.Event)
	// testTokenRoutes marks extra routes for tokens, by pattern. TEST ONLY: it exists so a test can prove that the
	// read-only subject alone refuses a write route that was marked by mistake. Production code never sets it.
	testTokenRoutes map[string]auth.Scope
}

// Names of the per-user limits in Server.Limits (also the key segment and metric label).
const (
	limitExec   = "exec"
	limitTail   = "tail"
	limitSearch = "search"
	// limitAgentChat holds the one running chat turn a user may have.
	limitAgentChat = "agent_chat"
)

// Names of the token buckets in Server.Rates.
const (
	rateLogSearch  = "logsearch"
	rateNotifyTest = "notifytest"
	rateSAMLLogin  = "samllogin"
	rateFocusOpen  = "focusopen"
	rateAPIToken   = "apitoken"
)

// The per-user bucket for requests authenticated by an access token (design: "Authentication"). Constants, not
// settings.
const (
	apiTokenBurst     = 120
	apiTokenPerSecond = 2
)

// allowRate takes a token from the named bucket. A limiter error allows the request: the fallback wrapper has already
// tried the per-task bucket, and a store problem must never block sign-in or a search.
func (s *Server) allowRate(ctx context.Context, name, key string, capacity int, refillPerSecond float64) bool {
	ok, err := s.Rates.Allow(ctx, name, key, capacity, refillPerSecond)
	return err != nil || ok
}

// ScanKicker wakes the scan loop so a request does not wait for the next interval. serve implements it over the signal
// bus, so any web task reaches the loop on the active coordinator; if nobody hears it a
// request waits at most HM_SCAN_INTERVAL.
type ScanKicker interface{ Kick() }

// New returns s ready to serve; it fills in defaults for Log and Now.
func New(s Server) *Server {
	s.costWarned = &warnedSet{}
	if s.rateForClass == nil {
		s.rateForClass = cost.RateForClass
	}
	s.focusOpens = &focusOpens{}
	s.routeLog = &routeRecorder{byMux: map[*http.ServeMux][]routeInfo{}}
	s.targetKeys, s.memberLines = &ttlCache[[]store.TargetFilter]{}, &ttlCache[string]{}
	s.agentReach = &agentReach{}
	s.draining, s.beginDrainOne = make(chan struct{}), &sync.Once{}
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
	if s.LogErrors == nil {
		s.LogErrors = logs.NewErrorCounter(s.Clients, s.Now)
		s.LogErrors.Pacer = s.Rates // the regional FilterLogEvents quota is shared by every task
	}
	if s.Rates == nil {
		s.Rates = limits.NewMemoryRate(s.Now)
	}
	if s.Limits == nil {
		s.Limits = limits.NewMemoryConcurrency()
	}
	if s.LogSearch == nil {
		s.LogSearch = &logs.Searcher{Clients: s.Clients, Now: s.Now, Timeout: s.Cfg.LogSearchTimeout, MaxBytes: int64(s.Cfg.LogSearchMaxGB) * logs.BytesPerGiB}
	}
	if s.Notify == nil {
		s.Notify = &notify.Senders{BaseURL: s.Cfg.BaseURL, SMTP: s.Cfg.Notify.SMTP,
			Guard: safedial.Policy{AllowedPrivate: s.Cfg.Notify.AllowedPrivateCIDRs}, Log: s.Log}
	}
	if s.NotifyBox == nil && s.Cfg.SessionKey != "" {
		s.NotifyBox, _ = secretbox.New(s.Cfg.SessionKey, secretbox.PurposeNotifySecret)
	}
	// plan 06
	if s.Telemetry == nil {
		s.Telemetry = telemetry.Default
	}
	// plan 07
	s.SAML = newSAMLService(s.SAML, &s)
	if s.OAuth == nil && s.oauthOn() {
		s.OAuth = oauthsrv.NewService(s.Store, s.Cfg)
		s.OAuth.Now = s.Now
	}
	if s.AgentHeartbeat <= 0 {
		s.AgentHeartbeat = 15 * time.Second
	}
	if s.AgentWriteTimeout <= 0 {
		s.AgentWriteTimeout = 10 * time.Second
	}
	if s.AgentRunSlack <= 0 {
		s.AgentRunSlack = 30 * time.Second
	}
	// plan 04: terminal
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

// oauthOn reports whether the OAuth authorization server is enabled. Config validation already requires HM_API_TOKENS
// and HM_MCP_PUBLIC_URL for HM_OAUTH; checking them again keeps a hand-built Config from half-enabling it.
func (s *Server) oauthOn() bool {
	return s.Cfg.OAuth && s.Cfg.APITokens && s.Cfg.MCPPublicURL != ""
}

func (s *Server) now() time.Time { return s.Now() }

// Handler builds the mux and wraps it:
// recover -> request log -> security headers -> auth.Middleware (for /api) -> token rate limit.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes(mux)
	var h http.Handler = mux
	h = s.tokenRequests(h)
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
	if scope, ok := s.testTokenRoutes[pattern]; ok {
		opts.Token = scope
	}
	s.routeLog.record(mux, routeInfo{Pattern: pattern, Opts: opts})
	mux.Handle(pattern, protect(h, opts))
}

// routeInfo is one route registered through handle, with its options.
type routeInfo struct {
	Pattern string
	Opts    routeOptions
}

// routeRecorder collects the routes handle registers on the muxes Routes is building. Handler's mux is never
// recorded.
type routeRecorder struct {
	mu    sync.Mutex
	byMux map[*http.ServeMux][]routeInfo
}

func (rr *routeRecorder) record(mux *http.ServeMux, info routeInfo) {
	if rr == nil {
		return
	}
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if list, ok := rr.byMux[mux]; ok {
		rr.byMux[mux] = append(list, info)
	}
}

// Routes lists every route registered through handle, with its options, in registration order. Routes registered
// directly on the mux (healthz, metrics, the dev fakes, the /api/ catch-all and the SPA) are not included. The token
// route tests and the MCP gateway's route table check use it.
func (s *Server) Routes() []routeInfo {
	if s.routeLog == nil {
		s.routeLog = &routeRecorder{byMux: map[*http.ServeMux][]routeInfo{}}
	}
	mux := http.NewServeMux()
	s.routeLog.mu.Lock()
	s.routeLog.byMux[mux] = []routeInfo{}
	s.routeLog.mu.Unlock()
	s.routes(mux)
	s.routeLog.mu.Lock()
	defer s.routeLog.mu.Unlock()
	list := s.routeLog.byMux[mux]
	delete(s.routeLog.byMux, mux)
	return list
}

// routes registers every route. Plans 02-05 append their handlers here.
func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.healthz)
	// plan 06: token-protected, deliberately outside session auth, CSRF and ETag.
	mux.Handle("GET /metrics", metricsHandler(s.MetricsToken, s.Telemetry, s.memberMetrics))

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
	if s.FakeNotify != nil {
		mux.Handle("/fake-notify/", s.FakeNotify)
	}
	s.handle(mux, "POST /api/auth/logout", s.logout, routeOptions{})
	s.handle(mux, "POST /api/auth/password", s.changePassword, routeOptions{})
	s.handle(mux, "GET /api/me", s.me, routeOptions{})
	s.handle(mux, "GET /api/overview", s.overview, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/clusters", s.listClusters, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/clusters/{clusterId}", s.getCluster, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services", s.listServicesFlat, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}", s.getService, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/tasks", s.listServiceTasks, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/operations", s.serviceOperations, routeOptions{Token: auth.ScopeRead})
	// Live page refresh: a heartbeat from a page that is open in a visible tab, and its release.
	s.handle(mux, "POST /api/focus", s.postFocus, routeOptions{NoETag: true})
	s.handle(mux, "DELETE /api/focus", s.deleteFocus, routeOptions{NoETag: true})
	s.handle(mux, "GET /api/tasks", s.listTasks, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/search", s.search, routeOptions{Token: auth.ScopeRead})
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
	s.networkRoutes(mux)      // plan 05
	s.storageRoutes(mux)      // plan 05
	s.analyticsRoutes(mux)    // analytics
	s.securityRoutes(mux)     // image security
	s.timelineRoutes(mux)     // service timeline
	s.offHoursRoutes(mux)     // off-hours schedules
	s.logSearchRoutes(mux)    // cross-service log search
	s.notificationRoutes(mux) // alert notifications

	s.configSearchRoutes(mux) // estate-wide config search
	if s.Cfg.APITokens {
		s.tokenRoutes(mux) // access tokens: absent (404) while HM_API_TOKENS is off
	} else if s.Cfg.AgentEnabled() {
		s.whoAmIRoute(mux) // a run token still needs whoami; nothing else of token management exists
	}
	if s.Cfg.AgentEnabled() {
		s.agentRoutes(mux) // the AI chat: absent (404) while HM_AGENT_URL is unset
		s.agentIdentityRoutes(mux)
	}
	if s.oauthOn() {
		s.oauthRoutes(mux) // OAuth for MCP connectors: absent (404) while HM_OAUTH is off
	}

	// Change-path reads (plan 02).
	s.handle(mux, "GET /api/services/{serviceId}/task-definition", s.getTaskDefinition, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/revisions", s.getRevisions, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/revisions/{revision}", s.getRevisionDefinition, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/image-tags", s.getImageTags, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/desired-count", s.getDesiredCount, routeOptions{Token: auth.ScopeRead})
	s.handle(mux, "GET /api/services/{serviceId}/deploy-settings", s.getDeploySettings, routeOptions{})
	s.handle(mux, "GET /api/services/{serviceId}/arm64-readiness", s.getARM64Readiness, routeOptions{Token: auth.ScopeRead})

	// Change-path writes (plan 02).
	s.handle(mux, "POST /api/services/{serviceId}/revisions", s.registerRevision, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/deploy", s.deployRevision, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/changeset", s.applyChangeSet, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/restart", s.restartService, routeOptions{})
	s.handle(mux, "POST /api/services/{serviceId}/desired-count", s.setDesiredCount, routeOptions{})
	s.handle(mux, "POST /api/tasks/{taskId}/stop", s.stopTask, routeOptions{})

	// Unknown API paths are JSON 404s and never fall through to the SPA. RequirePasswordChanged
	// still applies so a user with a pending password change sees password_change_required. An access
	// token is refused here like on any unmarked route, so it cannot probe which paths exist.
	mux.Handle("/api/", auth.RequireTokenScope("")(auth.RequirePasswordChanged(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Not found.")
	}))))

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

// BeginDrain tells running log searches and AI agent runs to stop; call it when Shutdown begins. Safe to call more than once,
// and a no-op on a Server that was not built by New.
func (s *Server) BeginDrain() {
	if s == nil || s.draining == nil || s.beginDrainOne == nil {
		return
	}
	s.beginDrainOne.Do(func() { close(s.draining) })
}
