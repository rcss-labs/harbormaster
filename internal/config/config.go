// Package config loads Harbormaster settings from HM_* environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/snapcache"
)

// PollConfig holds the polling intervals and concurrency limits for AWS reads.
type PollConfig struct {
	Services      time.Duration
	RunningTasks  time.Duration
	FullTasks     time.Duration
	StoppedTasks  time.Duration
	Clusters      time.Duration
	ServiceList   time.Duration
	CountsHistory time.Duration
	MaxInFlight   int
	Jitter        time.Duration
	TargetHealth  time.Duration
	Scaling       time.Duration
	Network       time.Duration // plan 05
	Storage       time.Duration // plan 05
	// IdleAfter is how long without session activity before polling goes idle; 0 disables idle mode.
	IdleAfter time.Duration
	// IdleInterval is the minimum wait between runs of every poller tier while idle.
	IdleInterval time.Duration
}

// RetentionConfig holds how long each kind of recorded data is kept.
type RetentionConfig struct {
	StoppedTasks      time.Duration
	ServiceEvents     time.Duration
	CountsHistory     time.Duration
	OperationLog      time.Duration
	DeploymentHistory time.Duration
	AuditLog          time.Duration
	OrphanTags        time.Duration
	// Daily analytics rollups, keyed by day rather than timestamp.
	AvailabilityDaily time.Duration
	CapacityDaily     time.Duration
	UtilDaily         time.Duration
	// Image scan results and the runs that produced them.
	ScanImages time.Duration
	ScanRuns   time.Duration
	// Alert notification delivery log and resolved incidents.
	NotificationLog time.Duration
}

// AnalyticsConfig toggles optional analytics collection.
type AnalyticsConfig struct {
	// Util enables the daily CloudWatch utilisation rollup (one GetMetricData batch per account and region a day).
	Util bool
}

// ScanConfig holds the image security scanner settings. The scanner runs as an
// on-demand ECS task launched with these launch settings.
type ScanConfig struct {
	Enabled           bool
	ClusterARN        string
	TaskDefinitionARN string
	Subnets           []string
	SecurityGroup     string
	Bucket            string
	RoleName          string
	Interval          time.Duration
	TaskTimeout       time.Duration
	DBRefresh         time.Duration
	DBMaxAge          time.Duration
	MaxTasks          int
	BatchSize         int
	MaxImageMB        int
}

// ARM64Config holds the settings of the image platform check behind the ARM64 advisor.
type ARM64Config struct {
	// Check turns the leader job on (HM_ARM64_CHECK).
	Check bool
	// CheckInterval is how often a cycle looks for new and due images (HM_ARM64_CHECK_INTERVAL).
	CheckInterval time.Duration
	// Recheck is how often a known image is resolved again (HM_ARM64_RECHECK).
	Recheck time.Duration
	// PublicRegistries lets the job read Docker Hub, public ECR and GHCR anonymously (HM_ARM64_PUBLIC_REGISTRIES).
	PublicRegistries bool
}

// OffHoursConfig holds the off-hours reconciler settings.
type OffHoursConfig struct {
	// Interval is how often the leader reconciles scheduled actions and capacity (HM_OFFHOURS_INTERVAL).
	Interval time.Duration
	// FakeDemo enables the seeded demo schedule in fake AWS mode (HM_FAKE_OFFHOURS_DEMO). It applies only when the
	// demo schedule is first seeded; an existing one is left as it is.
	FakeDemo bool
}

// Config is the complete runtime configuration for the server.
type Config struct {
	Listen                  string
	DatabaseURL             string
	DatabaseSecretARN       string
	SessionKey              string
	BaseURL                 string
	CookieSecure            bool
	BootstrapAdminSecretARN string
	FakeAWS                 bool
	TrustedProxy            bool
	LogLevel                string
	SessionIdle             time.Duration
	SessionAbsolute         time.Duration
	LoginMaxPerUser         int
	LoginMaxPerIP           int
	LoginWindow             time.Duration
	Poll                    PollConfig
	Retention               RetentionConfig
	Scan                    ScanConfig
	OffHours                OffHoursConfig
	ARM64                   ARM64Config
	Notify                  NotifyConfig
	Analytics               AnalyticsConfig
	PageSizeDefault         int
	PageSizeMax             int
	MetricsCacheTTL         time.Duration
	LogTailIdle             time.Duration
	LogTailsPerUser         int
	LogHistoryLimit         int
	LogSearchTimeout        time.Duration
	LogSearchMaxServices    int
	LogSearchMaxRange       time.Duration
	LogSearchMaxGB          int // scan cap per search in GiB; 0 = no cap
	MaxDesiredCount         int
	// LegacyLeaderGate makes a task that polls or coordinates take the old leader's advisory key in shared mode before
	// claiming any lease (HM_LEGACY_LEADER_GATE, default true). It keeps old and new versions from both acting during an
	// upgrade or a rollback. It is removed, with its pinned connection, in the release after the one that ships leases.
	LegacyLeaderGate bool

	// SAML: how long a sign-in request (and its binding cookie) stays valid
	SAMLRequestTTL time.Duration

	// plan 04: terminal
	ExecIdleTimeout        time.Duration
	ExecMaxDuration        time.Duration
	ExecMaxSessionsPerUser int

	// plan 06: metrics endpoint token; empty disables /metrics
	MetricsToken string

	// Access tokens (MCP phase 1). Everything is off unless APITokens is true: bearer tokens are ignored and the
	// token routes answer 404.
	APITokens             bool          // HM_API_TOKENS
	TokenMaxTTL           time.Duration // longest lifetime for a user who is neither SSO-linked nor an admin
	TokenMaxTTLSSO        time.Duration // longest lifetime for an SSO-linked user or an admin
	TokenMaxPerUser       int           // unexpired personal tokens per user
	TokenLogSearchMaxGB   int           // scan cap in GiB for one token-initiated log search
	TokenLogSearchDailyGB int           // scan budget in GiB per user per UTC day for token searches; 0 turns them off
	TokenRedactEnv        bool          // redact environment values in task definitions answered to tokens
	MCPPublicURL          string        // the MCP gateway's public base URL, shown in the UI's connect instructions

	// KV selects the shared Valkey or Redis store; an empty URL means none
	KV KVConfig

	// Role selects which parts this process runs (HM_ROLE, default all). The serve flag --role overrides it.
	Role Role
	// DBMaxConns caps the database pool (HM_DB_MAX_CONNS); 0 means the role's default (see PoolSize).
	DBMaxConns int

	// Focus configures live refresh of the cluster or service page that is on screen.
	Focus FocusConfig

	// SnapshotCache turns the versioned read-model cache on when a store is configured (HM_SNAPSHOT_CACHE);
	// SnapshotCacheTTL is how long a cached model lives (HM_SNAPSHOT_CACHE_TTL).
	SnapshotCache    bool
	SnapshotCacheTTL time.Duration
}

// FocusConfig holds the live page refresh settings (HM_FOCUS_*).
type FocusConfig struct {
	Interval, TTL                          time.Duration
	MaxObjects, MaxPerUser, MaxCallsPerSec int
}

// KVConfig holds the shared store settings. The URL can carry a password, so
// it is never put in an error or a log line.
type KVConfig struct {
	URL    string
	Prefix string
}

// envReader reads HM_* variables and remembers the first parse error, so Load
// can report one clear failure instead of checking after every field.
type envReader struct {
	getenv func(string) string
	err    error
}

func (r *envReader) text(name, fallback string) string {
	if value := r.getenv(name); value != "" {
		return value
	}
	return fallback
}

func (r *envReader) duration(name string, fallback time.Duration) time.Duration {
	raw := r.getenv(name)
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		r.fail(name, "invalid duration %q", raw)
		return fallback
	}
	return parsed
}

func (r *envReader) boolean(name string, fallback bool) bool {
	raw := r.getenv(name)
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		r.fail(name, "invalid boolean %q", raw)
		return fallback
	}
	return parsed
}

func (r *envReader) integer(name string, fallback int) int {
	raw := r.getenv(name)
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		r.fail(name, "invalid integer %q", raw)
		return fallback
	}
	return parsed
}

// list splits a comma-separated variable, trimming spaces and dropping empty items.
func (r *envReader) list(name string) []string {
	var items []string
	for _, item := range strings.Split(r.getenv(name), ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			items = append(items, trimmed)
		}
	}
	return items
}

func (r *envReader) fail(name, format string, args ...any) {
	if r.err == nil {
		r.err = fmt.Errorf("%s: %s", name, fmt.Sprintf(format, args...))
	}
}

// Load builds a Config from getenv, applying defaults for unset or empty
// variables. It returns an error for malformed values or when no database is
// configured.
func Load(getenv func(string) string) (Config, error) {
	reader := &envReader{getenv: getenv}

	cfg := Config{
		Listen:                  reader.text("HM_LISTEN", ":8480"),
		DatabaseURL:             reader.text("HM_DATABASE_URL", ""),
		DatabaseSecretARN:       reader.text("HM_DATABASE_SECRET_ARN", ""),
		SessionKey:              reader.text("HM_SESSION_KEY", ""),
		BaseURL:                 reader.text("HM_BASE_URL", "http://127.0.0.1:8480"),
		CookieSecure:            reader.boolean("HM_COOKIE_SECURE", true),
		BootstrapAdminSecretARN: reader.text("HM_BOOTSTRAP_ADMIN_PASSWORD_SECRET_ARN", ""),
		FakeAWS:                 reader.boolean("HM_FAKE_AWS", false),
		TrustedProxy:            reader.boolean("HM_TRUSTED_PROXY", false),
		LogLevel:                reader.text("HM_LOG_LEVEL", "info"),
		SessionIdle:             reader.duration("HM_SESSION_IDLE", 12*time.Hour),
		SessionAbsolute:         reader.duration("HM_SESSION_ABSOLUTE", 168*time.Hour),
		LoginMaxPerUser:         reader.integer("HM_LOGIN_MAX_PER_USER", 5),
		LoginMaxPerIP:           reader.integer("HM_LOGIN_MAX_PER_IP", 30),
		LoginWindow:             reader.duration("HM_LOGIN_WINDOW", 15*time.Minute),
		Poll: PollConfig{
			Services:      reader.duration("HM_POLL_SERVICES", 30*time.Second),
			RunningTasks:  reader.duration("HM_POLL_RUNNING_TASKS", 30*time.Second),
			FullTasks:     reader.duration("HM_POLL_FULL_TASKS", 5*time.Minute),
			StoppedTasks:  reader.duration("HM_POLL_STOPPED_TASKS", 60*time.Second),
			Clusters:      reader.duration("HM_POLL_CLUSTERS", 60*time.Second),
			ServiceList:   reader.duration("HM_POLL_SERVICE_LIST", 60*time.Second),
			CountsHistory: reader.duration("HM_POLL_COUNTS_HISTORY", 5*time.Minute),
			MaxInFlight:   reader.integer("HM_POLL_MAX_INFLIGHT", 8),
			Jitter:        reader.duration("HM_POLL_JITTER", 500*time.Millisecond),
			TargetHealth:  reader.duration("HM_POLL_TARGET_HEALTH", 60*time.Second),
			Scaling:       reader.duration("HM_POLL_SCALING", 5*time.Minute),
			Network:       reader.duration("HM_POLL_NETWORK", 5*time.Minute), // plan 05
			Storage:       reader.duration("HM_POLL_STORAGE", 5*time.Minute), // plan 05
			IdleAfter:     reader.duration("HM_POLL_IDLE_AFTER", 10*time.Minute),
			IdleInterval:  reader.duration("HM_POLL_IDLE_INTERVAL", 5*time.Minute),
		},
		Retention: RetentionConfig{
			StoppedTasks:      reader.duration("HM_RETENTION_STOPPED_TASKS", 168*time.Hour),
			ServiceEvents:     reader.duration("HM_RETENTION_SERVICE_EVENTS", 720*time.Hour),
			CountsHistory:     reader.duration("HM_RETENTION_COUNTS_HISTORY", 168*time.Hour),
			OperationLog:      reader.duration("HM_RETENTION_OPERATION_LOG", 2160*time.Hour),
			DeploymentHistory: reader.duration("HM_RETENTION_DEPLOYMENT_HISTORY", 2160*time.Hour),
			AuditLog:          reader.duration("HM_RETENTION_AUDIT_LOG", 8760*time.Hour),
			OrphanTags:        reader.duration("HM_RETENTION_ORPHAN_TAGS", 720*time.Hour),
			AvailabilityDaily: reader.duration("HM_RETENTION_AVAILABILITY_DAILY", 8760*time.Hour),
			CapacityDaily:     reader.duration("HM_RETENTION_CAPACITY_DAILY", 8760*time.Hour),
			UtilDaily:         reader.duration("HM_RETENTION_UTIL_DAILY", 2160*time.Hour),
			ScanImages:        reader.duration("HM_RETENTION_SCAN_IMAGES", 168*time.Hour),
			ScanRuns:          reader.duration("HM_RETENTION_SCAN_RUNS", 720*time.Hour),
			NotificationLog:   reader.duration("HM_RETENTION_NOTIFICATION_LOG", 720*time.Hour),
		},
		Scan: ScanConfig{
			Enabled:           reader.boolean("HM_SCAN_ENABLED", false),
			ClusterARN:        reader.text("HM_SCAN_CLUSTER_ARN", ""),
			TaskDefinitionARN: reader.text("HM_SCAN_TASK_DEFINITION", ""),
			Subnets:           reader.list("HM_SCAN_SUBNETS"),
			SecurityGroup:     reader.text("HM_SCAN_SECURITY_GROUP", ""),
			Bucket:            reader.text("HM_SCAN_BUCKET", ""),
			RoleName:          reader.text("HM_SCAN_ROLE_NAME", "harbormaster-scan"),
			Interval:          reader.duration("HM_SCAN_INTERVAL", 15*time.Second),
			TaskTimeout:       reader.duration("HM_SCAN_TASK_TIMEOUT", 30*time.Minute),
			DBRefresh:         reader.duration("HM_SCAN_DB_REFRESH", 24*time.Hour),
			DBMaxAge:          reader.duration("HM_SCAN_DB_MAX_AGE", 120*time.Hour),
			MaxTasks:          reader.integer("HM_SCAN_MAX_TASKS", 1),
			BatchSize:         reader.integer("HM_SCAN_BATCH_SIZE", 25),
			MaxImageMB:        reader.integer("HM_SCAN_MAX_IMAGE_MB", 4096),
		},
		OffHours: OffHoursConfig{
			Interval: reader.duration("HM_OFFHOURS_INTERVAL", 60*time.Second),
			FakeDemo: reader.boolean("HM_FAKE_OFFHOURS_DEMO", true),
		},
		ARM64: ARM64Config{
			Check:            reader.boolean("HM_ARM64_CHECK", true),
			CheckInterval:    reader.duration("HM_ARM64_CHECK_INTERVAL", 15*time.Minute),
			Recheck:          reader.duration("HM_ARM64_RECHECK", 24*time.Hour),
			PublicRegistries: reader.boolean("HM_ARM64_PUBLIC_REGISTRIES", true),
		},
		Notify:               readNotify(reader),
		Analytics:            AnalyticsConfig{Util: reader.boolean("HM_ANALYTICS_UTIL", true)},
		PageSizeDefault:      reader.integer("HM_PAGE_SIZE_DEFAULT", 100),
		PageSizeMax:          reader.integer("HM_PAGE_SIZE_MAX", 500),
		MetricsCacheTTL:      reader.duration("HM_METRICS_CACHE_TTL", 60*time.Second),
		LogTailIdle:          reader.duration("HM_LOG_TAIL_IDLE", 30*time.Minute),
		LogTailsPerUser:      reader.integer("HM_LOG_TAILS_PER_USER", 2),
		LogHistoryLimit:      reader.integer("HM_LOG_HISTORY_LIMIT", 500),
		LogSearchTimeout:     reader.duration("HM_LOG_SEARCH_TIMEOUT", 60*time.Second),
		LogSearchMaxServices: reader.integer("HM_LOG_SEARCH_MAX_SERVICES", 20),
		LogSearchMaxRange:    reader.duration("HM_LOG_SEARCH_MAX_RANGE", 168*time.Hour),
		LogSearchMaxGB:       reader.integer("HM_LOG_SEARCH_MAX_GB", 10),
		MaxDesiredCount:      reader.integer("HM_MAX_DESIRED_COUNT", 50),
		LegacyLeaderGate:     reader.boolean("HM_LEGACY_LEADER_GATE", true),
		SAMLRequestTTL:       reader.duration("HM_SAML_REQUEST_TTL", 10*time.Minute),

		// plan 04: terminal
		ExecIdleTimeout:        reader.duration("HM_EXEC_IDLE_TIMEOUT", 20*time.Minute),
		ExecMaxDuration:        reader.duration("HM_EXEC_MAX_DURATION", 4*time.Hour),
		ExecMaxSessionsPerUser: reader.integer("HM_EXEC_MAX_SESSIONS_PER_USER", 3),

		// plan 06: metrics endpoint token
		MetricsToken: strings.TrimSpace(reader.text("HM_METRICS_TOKEN", "")),

		// MCP phase 1: access tokens
		APITokens:             reader.boolean("HM_API_TOKENS", false),
		TokenMaxTTL:           reader.duration("HM_TOKEN_MAX_TTL", 2160*time.Hour),
		TokenMaxTTLSSO:        reader.duration("HM_TOKEN_MAX_TTL_SSO", 168*time.Hour),
		TokenMaxPerUser:       reader.integer("HM_TOKEN_MAX_PER_USER", 10),
		TokenLogSearchMaxGB:   reader.integer("HM_TOKEN_LOG_SEARCH_MAX_GB", 2),
		TokenLogSearchDailyGB: reader.integer("HM_TOKEN_LOG_SEARCH_DAILY_GB", 20),
		TokenRedactEnv:        reader.boolean("HM_TOKEN_REDACT_ENV", false),
		MCPPublicURL:          strings.TrimSpace(reader.text("HM_MCP_PUBLIC_URL", "")),

		KV: KVConfig{
			URL:    strings.TrimSpace(reader.text("HM_KV_URL", "")),
			Prefix: reader.text("HM_KV_PREFIX", "hm"),
		},
	}

	if reader.err != nil {
		return Config{}, reader.err
	}
	role, err := ParseRole(reader.text("HM_ROLE", ""))
	if err != nil {
		return Config{}, err
	}
	cfg.Role = role
	cfg.Focus = FocusConfig{
		Interval:       reader.duration("HM_FOCUS_INTERVAL", 5*time.Second),
		TTL:            reader.duration("HM_FOCUS_TTL", 30*time.Second),
		MaxObjects:     reader.integer("HM_FOCUS_MAX_OBJECTS", 50),
		MaxPerUser:     reader.integer("HM_FOCUS_MAX_PER_USER", 5),
		MaxCallsPerSec: reader.integer("HM_FOCUS_MAX_CALLS_PER_SEC", 5),
	}
	cfg.DBMaxConns = reader.integer("HM_DB_MAX_CONNS", 0)
	cfg.SnapshotCache = reader.boolean("HM_SNAPSHOT_CACHE", true)
	cfg.SnapshotCacheTTL = reader.duration("HM_SNAPSHOT_CACHE_TTL", 30*time.Second)
	if reader.err != nil {
		return Config{}, reader.err
	}
	if cfg.DBMaxConns != 0 && cfg.DBMaxConns < minDBMaxConns {
		return Config{}, errors.New("HM_DB_MAX_CONNS: must be 0 (the role's default) or at least 3")
	}
	if err := validateFocus(cfg.Focus); err != nil {
		return Config{}, err
	}
	if cfg.SnapshotCacheTTL < time.Second || cfg.SnapshotCacheTTL > snapcache.TokenTTL {
		return Config{}, errors.New("HM_SNAPSHOT_CACHE_TTL: must be between 1s and 10m (the life of a generation token)")
	}
	if cfg.MaxDesiredCount < 1 {
		return Config{}, errors.New("HM_MAX_DESIRED_COUNT: must be at least 1")
	}
	if cfg.LogSearchTimeout <= 0 {
		return Config{}, errors.New("HM_LOG_SEARCH_TIMEOUT: must be positive")
	}
	if cfg.LogSearchMaxServices < 1 {
		return Config{}, errors.New("HM_LOG_SEARCH_MAX_SERVICES: must be at least 1")
	}
	if cfg.LogSearchMaxRange <= 0 {
		return Config{}, errors.New("HM_LOG_SEARCH_MAX_RANGE: must be positive")
	}
	if cfg.LogSearchMaxGB < 0 {
		return Config{}, errors.New("HM_LOG_SEARCH_MAX_GB: must be 0 (no cap) or positive")
	}
	if cfg.SAMLRequestTTL < time.Minute || cfg.SAMLRequestTTL > 30*time.Minute {
		return Config{}, errors.New("HM_SAML_REQUEST_TTL: must be between 1m and 30m")
	}
	if cfg.Poll.IdleAfter < 0 {
		return Config{}, errors.New("HM_POLL_IDLE_AFTER: must be 0 (disabled) or positive")
	}
	if cfg.Poll.IdleAfter > 0 && cfg.Poll.IdleInterval < time.Second {
		return Config{}, errors.New("HM_POLL_IDLE_INTERVAL: must be at least 1s when idle mode is enabled")
	}
	if cfg.OffHours.Interval < time.Second {
		return Config{}, errors.New("HM_OFFHOURS_INTERVAL: must be at least 1s")
	}
	if cfg.ARM64.CheckInterval < time.Second {
		return Config{}, errors.New("HM_ARM64_CHECK_INTERVAL: must be at least 1s")
	}
	if cfg.ARM64.Recheck <= 0 {
		return Config{}, errors.New("HM_ARM64_RECHECK: must be positive")
	}
	if err := validateNotify(cfg.Notify); err != nil {
		return Config{}, err
	}
	if err := validateScan(cfg.Scan); err != nil {
		return Config{}, err
	}
	if err := validateKV(cfg.KV); err != nil {
		return Config{}, err
	}
	if err := validateTokens(cfg); err != nil {
		return Config{}, err
	}
	if cfg.DatabaseURL == "" && cfg.DatabaseSecretARN == "" {
		return Config{}, errors.New("HM_DATABASE_URL or HM_DATABASE_SECRET_ARN must be set")
	}
	return cfg, nil
}

// validateTokens checks the access token settings. They are checked whether or not HM_API_TOKENS is on, so a bad
// value is caught before anyone turns tokens on.
func validateTokens(cfg Config) error {
	if cfg.TokenMaxTTL <= 0 {
		return errors.New("HM_TOKEN_MAX_TTL: must be positive")
	}
	if cfg.TokenMaxTTLSSO <= 0 {
		return errors.New("HM_TOKEN_MAX_TTL_SSO: must be positive")
	}
	if cfg.TokenMaxTTLSSO > cfg.TokenMaxTTL {
		return errors.New("HM_TOKEN_MAX_TTL_SSO: must not be above HM_TOKEN_MAX_TTL")
	}
	if cfg.TokenMaxPerUser < 1 {
		return errors.New("HM_TOKEN_MAX_PER_USER: must be at least 1")
	}
	if cfg.TokenLogSearchMaxGB < 0 {
		return errors.New("HM_TOKEN_LOG_SEARCH_MAX_GB: must be 0 or positive")
	}
	if cfg.TokenLogSearchDailyGB < 0 {
		return errors.New("HM_TOKEN_LOG_SEARCH_DAILY_GB: must be 0 (token log search off) or positive")
	}
	if cfg.MCPPublicURL != "" {
		parsed, err := url.Parse(cfg.MCPPublicURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" ||
			strings.HasSuffix(cfg.MCPPublicURL, "/") || strings.ContainsAny(cfg.MCPPublicURL, "?#") {
			return errors.New("HM_MCP_PUBLIC_URL: must be an https:// URL with no path, query or trailing slash")
		}
	}
	return nil
}

// ValidateScanLaunch checks the scanner launch settings. It is separate from Load
// because --fake-aws is applied after Load, and fake mode needs none of them.
// Call it once the final FakeAWS value is known. Enabled against real AWS, every
// launch setting is required and the error names the first one that is missing.
func (c Config) ValidateScanLaunch() error {
	if !c.Scan.Enabled || c.FakeAWS {
		return nil
	}
	required := []struct {
		name  string
		value string
	}{
		{"HM_SCAN_CLUSTER_ARN", c.Scan.ClusterARN},
		{"HM_SCAN_TASK_DEFINITION", c.Scan.TaskDefinitionARN},
		{"HM_SCAN_SUBNETS", strings.Join(c.Scan.Subnets, ",")},
		{"HM_SCAN_SECURITY_GROUP", c.Scan.SecurityGroup},
		{"HM_SCAN_BUCKET", c.Scan.Bucket},
	}
	for _, setting := range required {
		if setting.value == "" {
			return fmt.Errorf("%s: required when HM_SCAN_ENABLED is true", setting.name)
		}
	}
	return nil
}

// validateScan checks the limits on the scanner settings. They apply whether or
// not the scanner is enabled, so a bad value is caught at startup either way.
func validateScan(scan ScanConfig) error {
	if scan.MaxTasks < 1 || scan.MaxTasks > 4 {
		return errors.New("HM_SCAN_MAX_TASKS: must be between 1 and 4")
	}
	if scan.BatchSize < 1 || scan.BatchSize > 100 {
		return errors.New("HM_SCAN_BATCH_SIZE: must be between 1 and 100")
	}
	if scan.TaskTimeout < 5*time.Minute {
		return errors.New("HM_SCAN_TASK_TIMEOUT: must be at least 5m")
	}
	if scan.Interval < time.Second {
		return errors.New("HM_SCAN_INTERVAL: must be at least 1s")
	}
	if scan.DBRefresh != 0 && scan.DBRefresh < time.Hour {
		return errors.New("HM_SCAN_DB_REFRESH: must be 0 (never refresh) or at least 1h")
	}
	if scan.MaxImageMB < 1 {
		return errors.New("HM_SCAN_MAX_IMAGE_MB: must be at least 1")
	}
	if scan.DBMaxAge < time.Hour {
		return errors.New("HM_SCAN_DB_MAX_AGE: must be at least 1h")
	}
	return nil
}

func validateFocus(f FocusConfig) error {
	switch {
	case f.Interval < 2*time.Second:
		return errors.New("HM_FOCUS_INTERVAL: must be at least 2s")
	case f.TTL <= focus.ReleaseGrace:
		return errors.New("HM_FOCUS_TTL: must be longer than 12s")
	case f.MaxObjects < 1:
		return errors.New("HM_FOCUS_MAX_OBJECTS: must be at least 1")
	case f.MaxPerUser < 1:
		return errors.New("HM_FOCUS_MAX_PER_USER: must be at least 1")
	case f.MaxCallsPerSec < 1:
		return errors.New("HM_FOCUS_MAX_CALLS_PER_SEC: must be at least 1")
	}
	return nil
}

var kvPrefixPattern = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// validateKV never echoes the URL: it can hold a password.
func validateKV(kv KVConfig) error {
	if kv.URL != "" {
		scheme, _, found := strings.Cut(kv.URL, "://")
		switch strings.ToLower(scheme) {
		case "redis", "rediss", "valkey", "valkeys":
			if !found {
				return errors.New("HM_KV_URL: scheme must be redis, rediss, valkey or valkeys")
			}
		default:
			return errors.New("HM_KV_URL: scheme must be redis, rediss, valkey or valkeys")
		}
	}
	// A database number would make the client send SELECT, which is not in the command allow-list.
	if kv.URL != "" {
		_, rest, _ := strings.Cut(kv.URL, "://")
		if _, afterHost, found := strings.Cut(rest, "/"); found {
			path, _, _ := strings.Cut(afterHost, "?")
			if path != "" && path != "0" {
				return errors.New("HM_KV_URL must use database 0")
			}
		}
	}
	if !kvPrefixPattern.MatchString(kv.Prefix) {
		return errors.New("HM_KV_PREFIX: use 1 to 32 lowercase letters, digits, - or _")
	}
	return nil
}
