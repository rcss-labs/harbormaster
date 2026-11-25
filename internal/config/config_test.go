package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"
)

func envFromMap(values map[string]string) func(string) string {
	return func(name string) string {
		return values[name]
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL": "postgres://example",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	want := Config{
		Listen:                  ":8480",
		DatabaseURL:             "postgres://example",
		DatabaseSecretARN:       "",
		SessionKey:              "",
		BaseURL:                 "http://127.0.0.1:8480",
		CookieSecure:            true,
		BootstrapAdminSecretARN: "",
		FakeAWS:                 false,
		TrustedProxy:            false,
		LogLevel:                "info",
		SessionIdle:             12 * time.Hour,
		SessionAbsolute:         168 * time.Hour,
		LoginMaxPerUser:         5,
		LoginMaxPerIP:           30,
		LoginWindow:             15 * time.Minute,
		Poll: PollConfig{
			Services:      30 * time.Second,
			RunningTasks:  30 * time.Second,
			FullTasks:     5 * time.Minute,
			StoppedTasks:  60 * time.Second,
			Clusters:      60 * time.Second,
			ServiceList:   60 * time.Second,
			CountsHistory: 5 * time.Minute,
			MaxInFlight:   8,
			Jitter:        500 * time.Millisecond,
			TargetHealth:  60 * time.Second,
			Scaling:       5 * time.Minute,
			Network:       5 * time.Minute,
			Storage:       5 * time.Minute,
			IdleAfter:     10 * time.Minute,
			IdleInterval:  5 * time.Minute,
		},
		Retention: RetentionConfig{
			StoppedTasks:       168 * time.Hour,
			ServiceEvents:      720 * time.Hour,
			CountsHistory:      168 * time.Hour,
			OperationLog:       2160 * time.Hour,
			DeploymentHistory:  2160 * time.Hour,
			AuditLog:           8760 * time.Hour,
			OrphanTags:         720 * time.Hour,
			AvailabilityDaily:  8760 * time.Hour,
			CapacityDaily:      8760 * time.Hour,
			UtilDaily:          2160 * time.Hour,
			ScanImages:         168 * time.Hour,
			ScanRuns:           720 * time.Hour,
			NotificationLog:    720 * time.Hour,
			AgentConversations: 720 * time.Hour,
			AgentRuns:          2160 * time.Hour,
		},
		Scan: ScanConfig{
			RoleName:    "harbormaster-scan",
			Interval:    15 * time.Second,
			TaskTimeout: 30 * time.Minute,
			DBRefresh:   24 * time.Hour,
			DBMaxAge:    120 * time.Hour,
			MaxTasks:    1,
			BatchSize:   25,
			MaxImageMB:  4096,
		},
		OffHours:         OffHoursConfig{Interval: 60 * time.Second, FakeDemo: true},
		ARM64:            ARM64Config{Check: true, CheckInterval: 15 * time.Minute, Recheck: 24 * time.Hour, PublicRegistries: true},
		Notify:           NotifyConfig{Interval: 10 * time.Second, Settle: 3 * time.Minute, FakeDemo: true, SMTP: SMTPConfig{Port: 587, TLS: "starttls"}},
		Analytics:        AnalyticsConfig{Util: true},
		KV:               KVConfig{Prefix: "hm"},
		Role:             RoleAll,
		Focus:            FocusConfig{Interval: 5 * time.Second, TTL: 30 * time.Second, MaxObjects: 50, MaxPerUser: 5, MaxCallsPerSec: 5},
		SnapshotCache:    true,
		SnapshotCacheTTL: 30 * time.Second,
		TokenMaxTTL:      2160 * time.Hour, TokenMaxTTLSSO: 168 * time.Hour, TokenMaxPerUser: 10,
		TokenLogSearchMaxGB: 2, TokenLogSearchDailyGB: 20,
		OAuthAccessTTL: time.Hour, AgentRunTokenTTL: 15 * time.Minute,
		AgentIdentityRatePerSecond: 5, AgentIdentityLogSearchMaxGB: 2, AgentIdentityLogSearchDailyGB: 20,
		OAuthClientHosts: []string{"claude.ai"},
		OAuthRedirectURIs: []string{
			"https://claude.ai/api/mcp/auth_callback", "https://claude.com/api/mcp/auth_callback",
			"http://localhost/callback", "http://127.0.0.1/callback",
		},
		PageSizeDefault:      100,
		PageSizeMax:          500,
		MetricsCacheTTL:      60 * time.Second,
		LogTailIdle:          30 * time.Minute,
		LogTailsPerUser:      2,
		LogHistoryLimit:      500,
		LogSearchTimeout:     60 * time.Second,
		LogSearchMaxServices: 20,
		LogSearchMaxRange:    168 * time.Hour,
		LogSearchMaxGB:       10,
		MaxDesiredCount:      50,
		LegacyLeaderGate:     true,
		SAMLRequestTTL:       10 * time.Minute,
		// plan 04: terminal
		ExecIdleTimeout:        20 * time.Minute,
		ExecMaxDuration:        4 * time.Hour,
		ExecMaxSessionsPerUser: 3,
	}

	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("Load defaults mismatch\n got: %+v\nwant: %+v", cfg, want)
	}
}

func TestLoad_ParsesDurationsAndBools(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":  "postgres://example",
		"HM_POLL_SERVICES": "7s",
		"HM_COOKIE_SECURE": "false",
		"HM_LISTEN":        "127.0.0.1:9000",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if cfg.Poll.Services != 7*time.Second {
		t.Errorf("Poll.Services = %v, want 7s", cfg.Poll.Services)
	}
	if cfg.CookieSecure {
		t.Errorf("CookieSecure = true, want false")
	}
	if cfg.Listen != "127.0.0.1:9000" {
		t.Errorf("Listen = %q, want 127.0.0.1:9000", cfg.Listen)
	}
}

func TestConfig_LegacyLeaderGateDefaultsTrue(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.LegacyLeaderGate {
		t.Error("LegacyLeaderGate = false by default, want true")
	}
	cfg, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_LEGACY_LEADER_GATE": "false"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LegacyLeaderGate {
		t.Error("LegacyLeaderGate = true with HM_LEGACY_LEADER_GATE=false")
	}
}

func TestConfig_SnapshotCacheDefaults(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SnapshotCache || cfg.SnapshotCacheTTL != 30*time.Second {
		t.Fatalf("defaults = %v, %v; want true, 30s", cfg.SnapshotCache, cfg.SnapshotCacheTTL)
	}
	cfg, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_SNAPSHOT_CACHE": "false", "HM_SNAPSHOT_CACHE_TTL": "5s"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SnapshotCache || cfg.SnapshotCacheTTL != 5*time.Second {
		t.Fatalf("overrides = %v, %v; want false, 5s", cfg.SnapshotCache, cfg.SnapshotCacheTTL)
	}
}

func TestConfig_SnapshotCacheTTLMustBeAtLeastOneSecond(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_SNAPSHOT_CACHE_TTL": "500ms"}))
	if err == nil || !strings.Contains(err.Error(), "HM_SNAPSHOT_CACHE_TTL") {
		t.Fatalf("error = %v, want one naming HM_SNAPSHOT_CACHE_TTL", err)
	}
}

// A cached model must not outlive the generation token that names it by more than the token's own life (ten minutes):
// the TTL is the bound on how stale a deleted cluster's page can be, so it is capped there.
func TestConfig_SnapshotCacheTTLIsCappedAtTheTokenLife(t *testing.T) {
	for _, ttl := range []string{"10m1s", "11m", "1h"} {
		_, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_SNAPSHOT_CACHE_TTL": ttl}))
		if err == nil || !strings.Contains(err.Error(), "HM_SNAPSHOT_CACHE_TTL") {
			t.Errorf("TTL %s: error = %v, want one naming HM_SNAPSHOT_CACHE_TTL", ttl, err)
		}
	}
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_SNAPSHOT_CACHE_TTL": "10m"}))
	if err != nil || cfg.SnapshotCacheTTL != 10*time.Minute {
		t.Fatalf("TTL 10m = %v, %v; want it accepted", cfg.SnapshotCacheTTL, err)
	}
}

func TestLoad_RequiresDatabase(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{}))
	if err == nil {
		t.Fatal("Load returned nil error without a database configured")
	}
	if !strings.Contains(err.Error(), "HM_DATABASE_URL") {
		t.Errorf("error %q does not mention HM_DATABASE_URL", err)
	}
}

func TestLoad_AcceptsSecretARNInsteadOfURL(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_SECRET_ARN": "arn:aws:secretsmanager:us-east-1:123456789012:secret:db",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.DatabaseSecretARN == "" {
		t.Error("DatabaseSecretARN is empty")
	}
}

func TestLoad_RejectsBadDuration(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL": "postgres://example",
		"HM_SESSION_IDLE": "soon",
	}))
	if err == nil {
		t.Fatal("Load returned nil error for invalid duration")
	}
	if !strings.Contains(err.Error(), "HM_SESSION_IDLE") {
		t.Errorf("error %q does not name HM_SESSION_IDLE", err)
	}
}

func TestConfig_ObserveDefaults(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"MetricsCacheTTL", cfg.MetricsCacheTTL, 60 * time.Second},
		{"Poll.TargetHealth", cfg.Poll.TargetHealth, 60 * time.Second},
		{"Poll.Scaling", cfg.Poll.Scaling, 5 * time.Minute},
		{"LogTailIdle", cfg.LogTailIdle, 30 * time.Minute},
		{"LogTailsPerUser", cfg.LogTailsPerUser, 2},
		{"LogHistoryLimit", cfg.LogHistoryLimit, 500},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", check.name, check.got, check.want)
		}
	}
}

func TestLoad_RejectsBadObserveInteger(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":       "postgres://example",
		"HM_LOG_TAILS_PER_USER": "abc",
	}))
	if err == nil {
		t.Fatal("Load returned nil error for invalid integer")
	}
	if !strings.Contains(err.Error(), "HM_LOG_TAILS_PER_USER") {
		t.Errorf("error %q does not name HM_LOG_TAILS_PER_USER", err)
	}
}

func TestLoad_ExecDefaults(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecIdleTimeout != 20*time.Minute || cfg.ExecMaxDuration != 4*time.Hour || cfg.ExecMaxSessionsPerUser != 3 {
		t.Errorf("exec defaults = %v %v %d", cfg.ExecIdleTimeout, cfg.ExecMaxDuration, cfg.ExecMaxSessionsPerUser)
	}
}

func TestLoad_ExecOverrides(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":               "postgres://example",
		"HM_EXEC_IDLE_TIMEOUT":          "5m",
		"HM_EXEC_MAX_DURATION":          "30m",
		"HM_EXEC_MAX_SESSIONS_PER_USER": "7",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecIdleTimeout != 5*time.Minute || cfg.ExecMaxDuration != 30*time.Minute || cfg.ExecMaxSessionsPerUser != 7 {
		t.Errorf("exec overrides = %v %v %d", cfg.ExecIdleTimeout, cfg.ExecMaxDuration, cfg.ExecMaxSessionsPerUser)
	}
}

func TestLoad_TrustedProxy(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":  "postgres://example",
		"HM_TRUSTED_PROXY": "true",
	}))
	if err != nil || !cfg.TrustedProxy {
		t.Fatalf("TrustedProxy = %v, err %v; want true", cfg.TrustedProxy, err)
	}
}

func TestLoad_MaxDesiredCountDefault50(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MaxDesiredCount != 50 {
		t.Errorf("MaxDesiredCount = %d, want 50", cfg.MaxDesiredCount)
	}
}

func TestLoad_RejectsMaxDesiredCountBelowOne(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":      "postgres://example",
		"HM_MAX_DESIRED_COUNT": "0",
	}))
	if err == nil || !strings.Contains(err.Error(), "HM_MAX_DESIRED_COUNT") {
		t.Fatalf("err = %v, want one naming HM_MAX_DESIRED_COUNT", err)
	}
}

func TestLoad_PollIntervalsDefault5m(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Poll.Network != 5*time.Minute || cfg.Poll.Storage != 5*time.Minute {
		t.Fatalf("Poll.Network = %v, Poll.Storage = %v, want 5m each", cfg.Poll.Network, cfg.Poll.Storage)
	}
}

func TestLoad_PollNetworkIntervalParsed(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_POLL_NETWORK": "90s"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Poll.Network != 90*time.Second {
		t.Fatalf("Poll.Network = %v, want 90s", cfg.Poll.Network)
	}
	if _, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_POLL_STORAGE": "x"})); err == nil {
		t.Fatal("bad HM_POLL_STORAGE accepted, want error")
	}
}

// plan 06: metrics endpoint token
func TestLoad_MetricsToken(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":  "postgres://example",
		"HM_METRICS_TOKEN": "abc",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.MetricsToken != "abc" {
		t.Errorf("MetricsToken = %q, want abc", cfg.MetricsToken)
	}

	for raw, want := range map[string]string{"abc\n": "abc", "  ": ""} {
		cfg, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_METRICS_TOKEN": raw}))
		if err != nil {
			t.Fatalf("Load returned error: %v", err)
		}
		if cfg.MetricsToken != want {
			t.Errorf("MetricsToken for %q = %q, want %q", raw, cfg.MetricsToken, want)
		}
	}

	cfg, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error without HM_METRICS_TOKEN: %v", err)
	}
	if cfg.MetricsToken != "" {
		t.Errorf("MetricsToken = %q, want empty when unset", cfg.MetricsToken)
	}
}

func TestLoad_SAMLRequestTTL_Default(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.SAMLRequestTTL != 10*time.Minute {
		t.Errorf("SAMLRequestTTL = %v, want 10m", cfg.SAMLRequestTTL)
	}
}

func TestLoad_SAMLRequestTTL_Bounds(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "below minimum", raw: "30s", wantErr: true},
		{name: "above maximum", raw: "1h", wantErr: true},
		{name: "within bounds", raw: "5m", wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(envFromMap(map[string]string{
				"HM_DATABASE_URL":     "postgres://example",
				"HM_SAML_REQUEST_TTL": tt.raw,
			}))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load returned nil error for HM_SAML_REQUEST_TTL=%s", tt.raw)
				}
				if !strings.Contains(err.Error(), "HM_SAML_REQUEST_TTL: must be between 1m and 30m") {
					t.Errorf("error %q does not carry the bounds message", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			if cfg.SAMLRequestTTL != 5*time.Minute {
				t.Errorf("SAMLRequestTTL = %v, want 5m", cfg.SAMLRequestTTL)
			}
		})
	}
}

func TestLoad_ParsesAnalyticsUtilFalse(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":   "postgres://example",
		"HM_ANALYTICS_UTIL": "false",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Analytics.Util {
		t.Errorf("Analytics.Util = true, want false")
	}
}

func TestLoad_ScanDefaults(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Scan.Enabled {
		t.Error("Scan.Enabled = true, want false")
	}
	if cfg.Scan.RoleName != "harbormaster-scan" {
		t.Errorf("Scan.RoleName = %q, want harbormaster-scan", cfg.Scan.RoleName)
	}
	if cfg.Scan.Interval != 15*time.Second {
		t.Errorf("Scan.Interval = %v, want 15s", cfg.Scan.Interval)
	}
}

func TestLoad_ScanDBRefreshAcceptsZero(t *testing.T) {
	// 0 turns the refresh off, for an operator-run mirror.
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_SCAN_DB_REFRESH": "0"}))
	if err != nil {
		t.Fatalf("Load with HM_SCAN_DB_REFRESH=0 returned error: %v", err)
	}
	if cfg.Scan.DBRefresh != 0 {
		t.Errorf("Scan.DBRefresh = %v, want 0", cfg.Scan.DBRefresh)
	}
	_, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_SCAN_DB_REFRESH": "30m"}))
	if err == nil || !strings.Contains(err.Error(), "HM_SCAN_DB_REFRESH") {
		t.Fatalf("err = %v, want one naming HM_SCAN_DB_REFRESH", err)
	}
}

func TestValidateScanLaunch_NamesMissingLaunchSetting(t *testing.T) {
	launch := map[string]string{
		"HM_DATABASE_URL":         "postgres://example",
		"HM_SCAN_ENABLED":         "true",
		"HM_SCAN_CLUSTER_ARN":     "arn:aws:ecs:us-east-1:123456789012:cluster/scan",
		"HM_SCAN_TASK_DEFINITION": "scanner:1",
		"HM_SCAN_SUBNETS":         "subnet-a",
		"HM_SCAN_SECURITY_GROUP":  "sg-a",
		"HM_SCAN_BUCKET":          "scan-bucket",
	}
	tests := []struct {
		name    string
		missing string
	}{
		{name: "cluster ARN", missing: "HM_SCAN_CLUSTER_ARN"},
		{name: "task definition", missing: "HM_SCAN_TASK_DEFINITION"},
		{name: "subnets", missing: "HM_SCAN_SUBNETS"},
		{name: "security group", missing: "HM_SCAN_SECURITY_GROUP"},
		{name: "bucket", missing: "HM_SCAN_BUCKET"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			values := map[string]string{}
			for key, value := range launch {
				values[key] = value
			}
			delete(values, tt.missing)
			cfg, err := Load(envFromMap(values))
			if err != nil {
				t.Fatalf("Load returned error: %v", err)
			}
			err = cfg.ValidateScanLaunch()
			if err == nil || !strings.Contains(err.Error(), tt.missing) {
				t.Fatalf("ValidateScanLaunch = %v, want one naming %s", err, tt.missing)
			}
		})
	}
}

func TestValidateScanLaunch_FakeModeNeedsNothing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL": "postgres://example",
		"HM_SCAN_ENABLED": "true",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	// serve --fake-aws sets FakeAWS after Load, so validate with it applied the same way.
	cfg.FakeAWS = true
	if err := cfg.ValidateScanLaunch(); err != nil {
		t.Fatalf("ValidateScanLaunch in fake mode = %v, want nil", err)
	}
	if !cfg.Scan.Enabled {
		t.Error("Scan.Enabled = false, want true")
	}
}

func TestLoad_ScanSubnetsAreSplitAndTrimmed(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL": "postgres://example",
		"HM_SCAN_SUBNETS": " subnet-a , subnet-b,,subnet-c ",
	}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := []string{"subnet-a", "subnet-b", "subnet-c"}
	if !reflect.DeepEqual(cfg.Scan.Subnets, want) {
		t.Errorf("Scan.Subnets = %q, want %q", cfg.Scan.Subnets, want)
	}
}

func TestLoad_ScanRetention(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Retention.ScanImages != 168*time.Hour {
		t.Errorf("Retention.ScanImages = %v, want 168h", cfg.Retention.ScanImages)
	}
	if cfg.Retention.ScanRuns != 720*time.Hour {
		t.Errorf("Retention.ScanRuns = %v, want 720h", cfg.Retention.ScanRuns)
	}
}

func TestLoad_ScanLimitBounds(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		raw   string
		valid bool
	}{
		{name: "max tasks below range", key: "HM_SCAN_MAX_TASKS", raw: "0"},
		{name: "max tasks above range", key: "HM_SCAN_MAX_TASKS", raw: "5"},
		{name: "max tasks upper edge", key: "HM_SCAN_MAX_TASKS", raw: "4", valid: true},
		{name: "batch size below range", key: "HM_SCAN_BATCH_SIZE", raw: "0"},
		{name: "batch size above range", key: "HM_SCAN_BATCH_SIZE", raw: "101"},
		{name: "batch size upper edge", key: "HM_SCAN_BATCH_SIZE", raw: "100", valid: true},
		{name: "task timeout below minimum", key: "HM_SCAN_TASK_TIMEOUT", raw: "4m"},
		{name: "task timeout minimum edge", key: "HM_SCAN_TASK_TIMEOUT", raw: "5m", valid: true},
		{name: "interval below minimum", key: "HM_SCAN_INTERVAL", raw: "500ms"},
		{name: "interval minimum edge", key: "HM_SCAN_INTERVAL", raw: "1s", valid: true},
		{name: "db refresh below minimum", key: "HM_SCAN_DB_REFRESH", raw: "30m"},
		{name: "db refresh minimum edge", key: "HM_SCAN_DB_REFRESH", raw: "1h", valid: true},
		{name: "max image below minimum", key: "HM_SCAN_MAX_IMAGE_MB", raw: "0"},
		{name: "max image minimum edge", key: "HM_SCAN_MAX_IMAGE_MB", raw: "1", valid: true},
		{name: "db max age below minimum", key: "HM_SCAN_DB_MAX_AGE", raw: "30m"},
		{name: "db max age minimum edge", key: "HM_SCAN_DB_MAX_AGE", raw: "1h", valid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(envFromMap(map[string]string{
				"HM_DATABASE_URL": "postgres://example",
				tt.key:            tt.raw,
			}))
			if tt.valid {
				if err != nil {
					t.Fatalf("Load returned error for %s=%s: %v", tt.key, tt.raw, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("err = %v, want one naming %s", err, tt.key)
			}
		})
	}
}

func TestConfig_ARM64Defaults(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatal(err)
	}
	want := ARM64Config{Check: true, CheckInterval: 15 * time.Minute, Recheck: 24 * time.Hour, PublicRegistries: true}
	if cfg.ARM64 != want {
		t.Fatalf("ARM64 = %+v, want %+v", cfg.ARM64, want)
	}
	cfg, err = Load(envFromMap(map[string]string{
		"HM_DATABASE_URL": "postgres://example", "HM_ARM64_CHECK": "false", "HM_ARM64_CHECK_INTERVAL": "1m",
		"HM_ARM64_RECHECK": "48h", "HM_ARM64_PUBLIC_REGISTRIES": "false"}))
	if err != nil || cfg.ARM64 != (ARM64Config{CheckInterval: time.Minute, Recheck: 48 * time.Hour}) {
		t.Fatalf("ARM64 = %+v, %v; want every setting read", cfg.ARM64, err)
	}
	// The cycle interval is at least 1s, like the off-hours one; 1s itself is allowed.
	if _, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_ARM64_CHECK_INTERVAL": "1s"})); err != nil {
		t.Errorf("HM_ARM64_CHECK_INTERVAL=1s: %v, want it accepted", err)
	}
	if _, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_ARM64_CHECK_INTERVAL": "500ms"})); err == nil || !strings.Contains(err.Error(), "at least 1s") {
		t.Errorf("HM_ARM64_CHECK_INTERVAL=500ms: err = %v, want one saying at least 1s", err)
	}
	for _, key := range []string{"HM_ARM64_CHECK_INTERVAL", "HM_ARM64_RECHECK"} {
		for _, value := range []string{"0s", "-1m"} {
			_, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", key: value}))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%s: err = %v, want one naming the setting", key, value, err)
			}
		}
	}
}

func TestLoad_OffHoursDefaultsAndBounds(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.OffHours.Interval != 60*time.Second {
		t.Errorf("OffHours.Interval = %v, want 60s", cfg.OffHours.Interval)
	}
	if !cfg.OffHours.FakeDemo {
		t.Error("OffHours.FakeDemo = false, want true")
	}

	cfg, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_OFFHOURS_INTERVAL": "1s", "HM_FAKE_OFFHOURS_DEMO": "false"}))
	if err != nil {
		t.Fatalf("Load returned error for the 1s edge: %v", err)
	}
	if cfg.OffHours.Interval != time.Second || cfg.OffHours.FakeDemo {
		t.Errorf("OffHours = %+v, want 1s and no demo", cfg.OffHours)
	}

	_, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_OFFHOURS_INTERVAL": "500ms"}))
	if err == nil || !strings.Contains(err.Error(), "HM_OFFHOURS_INTERVAL") {
		t.Fatalf("err = %v, want one naming HM_OFFHOURS_INTERVAL", err)
	}
}

func TestConfig_LogSearchDefaults(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.LogSearchTimeout != 60*time.Second || cfg.LogSearchMaxServices != 20 || cfg.LogSearchMaxRange != 168*time.Hour {
		t.Errorf("log search defaults = %v %d %v, want 60s 20 168h", cfg.LogSearchTimeout, cfg.LogSearchMaxServices, cfg.LogSearchMaxRange)
	}
	for name, value := range map[string]string{
		"HM_LOG_SEARCH_TIMEOUT": "0s", "HM_LOG_SEARCH_MAX_SERVICES": "0", "HM_LOG_SEARCH_MAX_RANGE": "-1h",
	} {
		_, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", name: value}))
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s=%s: error %v, want one naming %s", name, value, err, name)
		}
	}
}

func TestLoad_PollIdleSettings(t *testing.T) {
	base := map[string]string{"HM_DATABASE_URL": "postgres://example"}
	with := func(extra map[string]string) map[string]string {
		env := map[string]string{}
		for k, v := range base {
			env[k] = v
		}
		for k, v := range extra {
			env[k] = v
		}
		return env
	}

	cfg, err := Load(envFromMap(with(map[string]string{"HM_POLL_IDLE_AFTER": "2m", "HM_POLL_IDLE_INTERVAL": "90s"})))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Poll.IdleAfter != 2*time.Minute || cfg.Poll.IdleInterval != 90*time.Second {
		t.Errorf("idle = %v/%v, want 2m/90s", cfg.Poll.IdleAfter, cfg.Poll.IdleInterval)
	}

	// 0 disables idle mode, and then the interval is not validated.
	if _, err := Load(envFromMap(with(map[string]string{"HM_POLL_IDLE_AFTER": "0", "HM_POLL_IDLE_INTERVAL": "0s"}))); err != nil {
		t.Errorf("idle disabled: Load: %v", err)
	}

	rejected := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"negative after", map[string]string{"HM_POLL_IDLE_AFTER": "-1m"}, "HM_POLL_IDLE_AFTER"},
		{"interval under 1s", map[string]string{"HM_POLL_IDLE_INTERVAL": "500ms"}, "HM_POLL_IDLE_INTERVAL"},
		{"bad after", map[string]string{"HM_POLL_IDLE_AFTER": "soon"}, "HM_POLL_IDLE_AFTER"},
	}
	for _, tc := range rejected {
		_, err := Load(envFromMap(with(tc.env)))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want mention of %s", tc.name, err, tc.want)
		}
	}
}

func TestConfig_LogSearchMaxGB(t *testing.T) {
	load := func(value string) (Config, error) {
		env := map[string]string{"HM_DATABASE_URL": "postgres://example"}
		if value != "" {
			env["HM_LOG_SEARCH_MAX_GB"] = value
		}
		return Load(envFromMap(env))
	}
	cfg, err := load("")
	if err != nil || cfg.LogSearchMaxGB != 10 {
		t.Fatalf("default = %d, %v; want 10", cfg.LogSearchMaxGB, err)
	}
	cfg, err = load("0")
	if err != nil || cfg.LogSearchMaxGB != 0 {
		t.Fatalf("0 = %d, %v; want 0 (no cap)", cfg.LogSearchMaxGB, err)
	}
	cfg, err = load("25")
	if err != nil || cfg.LogSearchMaxGB != 25 {
		t.Fatalf("25 = %d, %v", cfg.LogSearchMaxGB, err)
	}
	if _, err := load("-1"); err == nil || !strings.Contains(err.Error(), "HM_LOG_SEARCH_MAX_GB") {
		t.Errorf("-1: error %v, want one naming HM_LOG_SEARCH_MAX_GB", err)
	}
}

func tokenEnv(extra map[string]string) func(string) string {
	values := map[string]string{"HM_DATABASE_URL": "postgres://example"}
	for k, v := range extra {
		values[k] = v
	}
	return envFromMap(values)
}

func TestLoad_TokenDefaults(t *testing.T) {
	cfg, err := Load(tokenEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APITokens || cfg.TokenRedactEnv || cfg.MCPPublicURL != "" {
		t.Errorf("flags/url not off by default: %+v", cfg)
	}
	if cfg.TokenMaxTTL != 90*24*time.Hour || cfg.TokenMaxTTLSSO != 7*24*time.Hour {
		t.Errorf("ttl = %v / %v, want 90d / 7d", cfg.TokenMaxTTL, cfg.TokenMaxTTLSSO)
	}
	if cfg.TokenMaxPerUser != 10 || cfg.TokenLogSearchMaxGB != 2 || cfg.TokenLogSearchDailyGB != 20 {
		t.Errorf("limits = %d, %d, %d; want 10, 2, 20", cfg.TokenMaxPerUser, cfg.TokenLogSearchMaxGB, cfg.TokenLogSearchDailyGB)
	}
}

func TestLoad_TokenOverrides(t *testing.T) {
	cfg, err := Load(tokenEnv(map[string]string{
		"HM_API_TOKENS": "true", "HM_TOKEN_MAX_TTL": "48h", "HM_TOKEN_MAX_TTL_SSO": "24h", "HM_TOKEN_MAX_PER_USER": "3",
		"HM_TOKEN_LOG_SEARCH_MAX_GB": "0", "HM_TOKEN_LOG_SEARCH_DAILY_GB": "0", "HM_TOKEN_REDACT_ENV": "true",
		"HM_MCP_PUBLIC_URL": "https://mcp.example.com", "HM_MCP_GATEWAY_CIDRS": "10.0.8.0/22, 10.1.0.0/24",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.APITokens || !cfg.TokenRedactEnv || cfg.TokenMaxTTL != 48*time.Hour || cfg.TokenMaxTTLSSO != 24*time.Hour ||
		cfg.TokenMaxPerUser != 3 || cfg.TokenLogSearchMaxGB != 0 || cfg.TokenLogSearchDailyGB != 0 ||
		cfg.MCPPublicURL != "https://mcp.example.com" {
		t.Errorf("cfg = %+v", cfg)
	}
	if got := fmt.Sprint(cfg.MCPGatewayCIDRs); got != "[10.0.8.0/22 10.1.0.0/24]" {
		t.Errorf("MCPGatewayCIDRs = %s", got)
	}
}

func TestLoad_GatewayCIDRsDefaultEmpty(t *testing.T) {
	cfg, err := Load(tokenEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPGatewayCIDRs) != 0 {
		t.Errorf("MCPGatewayCIDRs = %v, want none", cfg.MCPGatewayCIDRs)
	}
}

func TestLoad_TokenValidation(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"zero ttl", map[string]string{"HM_TOKEN_MAX_TTL": "0s"}, "HM_TOKEN_MAX_TTL"},
		{"negative ttl", map[string]string{"HM_TOKEN_MAX_TTL": "-1h"}, "HM_TOKEN_MAX_TTL"},
		{"zero sso ttl", map[string]string{"HM_TOKEN_MAX_TTL_SSO": "0s"}, "HM_TOKEN_MAX_TTL_SSO"},
		{"sso above long", map[string]string{"HM_TOKEN_MAX_TTL": "24h", "HM_TOKEN_MAX_TTL_SSO": "48h"}, "HM_TOKEN_MAX_TTL_SSO"},
		{"per user zero", map[string]string{"HM_TOKEN_MAX_PER_USER": "0"}, "HM_TOKEN_MAX_PER_USER"},
		{"max gb negative", map[string]string{"HM_TOKEN_LOG_SEARCH_MAX_GB": "-1"}, "HM_TOKEN_LOG_SEARCH_MAX_GB"},
		{"daily gb negative", map[string]string{"HM_TOKEN_LOG_SEARCH_DAILY_GB": "-1"}, "HM_TOKEN_LOG_SEARCH_DAILY_GB"},
		{"url http", map[string]string{"HM_MCP_PUBLIC_URL": "http://mcp.example.com"}, "HM_MCP_PUBLIC_URL"},
		{"url path", map[string]string{"HM_MCP_PUBLIC_URL": "https://mcp.example.com/mcp"}, "HM_MCP_PUBLIC_URL"},
		{"url trailing slash", map[string]string{"HM_MCP_PUBLIC_URL": "https://mcp.example.com/"}, "HM_MCP_PUBLIC_URL"},
		{"url query", map[string]string{"HM_MCP_PUBLIC_URL": "https://mcp.example.com?a=b"}, "HM_MCP_PUBLIC_URL"},
		{"cidr not a prefix", map[string]string{"HM_MCP_GATEWAY_CIDRS": "10.0.0.1"}, "HM_MCP_GATEWAY_CIDRS"},
		{"cidr garbage", map[string]string{"HM_MCP_GATEWAY_CIDRS": "10.0.0.0/24,nope"}, "HM_MCP_GATEWAY_CIDRS"},
		{"cidr match-all v4", map[string]string{"HM_MCP_GATEWAY_CIDRS": "0.0.0.0/0"}, "HM_MCP_GATEWAY_CIDRS"},
		{"cidr match-all v6", map[string]string{"HM_MCP_GATEWAY_CIDRS": "::/0"}, "HM_MCP_GATEWAY_CIDRS"},
		{"url no host", map[string]string{"HM_MCP_PUBLIC_URL": "https://"}, "HM_MCP_PUBLIC_URL"},
		{"url bad port", map[string]string{"HM_MCP_PUBLIC_URL": "https://mcp.example.com:99999"}, "HM_MCP_PUBLIC_URL"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(tokenEnv(tc.env))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %s", err, tc.want)
			}
		})
	}
}

func TestLoad_OAuthDefaultsAndValidation(t *testing.T) {
	load := func(extra map[string]string) (Config, error) {
		env := map[string]string{"HM_DATABASE_URL": "postgres://example"}
		for k, v := range extra {
			env[k] = v
		}
		return Load(envFromMap(env))
	}

	cfg, err := load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.OAuth || cfg.OAuthAccessTTL != time.Hour {
		t.Fatalf("defaults: OAuth=%v ttl=%v, want off and 1h", cfg.OAuth, cfg.OAuthAccessTTL)
	}
	if !reflect.DeepEqual(cfg.OAuthClientHosts, []string{"claude.ai"}) {
		t.Fatalf("client hosts = %v, want [claude.ai]", cfg.OAuthClientHosts)
	}
	wantRedirects := []string{
		"https://claude.ai/api/mcp/auth_callback", "https://claude.com/api/mcp/auth_callback",
		"http://localhost/callback", "http://127.0.0.1/callback",
	}
	if !reflect.DeepEqual(cfg.OAuthRedirectURIs, wantRedirects) {
		t.Fatalf("redirect URIs = %v, want %v", cfg.OAuthRedirectURIs, wantRedirects)
	}

	on := map[string]string{"HM_OAUTH": "true", "HM_API_TOKENS": "true", "HM_MCP_PUBLIC_URL": "https://mcp.example.com"}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for key, val := range on {
			m[key] = val
		}
		m[k] = v
		return m
	}
	without := func(k string) map[string]string {
		m := map[string]string{}
		for key, val := range on {
			if key != k {
				m[key] = val
			}
		}
		return m
	}
	if cfg, err := load(on); err != nil || !cfg.OAuth {
		t.Fatalf("valid OAuth config: %v (OAuth=%v)", err, cfg.OAuth)
	}
	failures := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"needs api tokens", without("HM_API_TOKENS"), "HM_API_TOKENS"},
		{"needs public url", without("HM_MCP_PUBLIC_URL"), "HM_MCP_PUBLIC_URL"},
		{"ttl below 5m", with("HM_OAUTH_ACCESS_TTL", "4m"), "HM_OAUTH_ACCESS_TTL"},
		{"ttl above 24h", with("HM_OAUTH_ACCESS_TTL", "25h"), "HM_OAUTH_ACCESS_TTL"},
		{"host with scheme", with("HM_OAUTH_CLIENT_HOSTS", "https://claude.ai"), "HM_OAUTH_CLIENT_HOSTS"},
		{"host uppercase", with("HM_OAUTH_CLIENT_HOSTS", "Claude.ai"), "HM_OAUTH_CLIENT_HOSTS"},
		{"redirect http off loopback", with("HM_OAUTH_REDIRECT_URIS", "http://evil.example/cb"), "HM_OAUTH_REDIRECT_URIS"},
		{"redirect with query", with("HM_OAUTH_REDIRECT_URIS", "https://claude.ai/cb?x=1"), "HM_OAUTH_REDIRECT_URIS"},
		{"redirect with fragment", with("HM_OAUTH_REDIRECT_URIS", "https://claude.ai/cb#x"), "HM_OAUTH_REDIRECT_URIS"},
		{"redirect loopback with port", with("HM_OAUTH_REDIRECT_URIS", "http://localhost:8080/cb"), "HM_OAUTH_REDIRECT_URIS"},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(tc.env); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %s", err, tc.want)
			}
		})
	}
	// Bounds are inclusive.
	for _, ttl := range []string{"5m", "24h"} {
		if _, err := load(with("HM_OAUTH_ACCESS_TTL", ttl)); err != nil {
			t.Fatalf("ttl %s refused: %v", ttl, err)
		}
	}
	// Overrides replace the defaults.
	cfg, err = load(with("HM_OAUTH_CLIENT_HOSTS", "a.example,b.example"))
	if err != nil || !reflect.DeepEqual(cfg.OAuthClientHosts, []string{"a.example", "b.example"}) {
		t.Fatalf("hosts override = %v, %v", cfg.OAuthClientHosts, err)
	}
}

func TestLoad_AgentDefaults(t *testing.T) {
	cfg, err := Load(tokenEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentEnabled() || cfg.AgentURL != "" || cfg.AgentCAPEM != "" || cfg.AgentRunTokenTTL != 15*time.Minute {
		t.Errorf("agent defaults = %+v", cfg)
	}
	cfg, err = Load(tokenEnv(map[string]string{"HM_AGENT_URL": "https://agent.internal:8484", "HM_AGENT_RUN_TOKEN_TTL": "5m"}))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AgentEnabled() || cfg.AgentURL != "https://agent.internal:8484" || cfg.AgentRunTokenTTL != 5*time.Minute {
		t.Errorf("agent cfg = %+v", cfg)
	}
}

func TestLoad_AgentURLValidation(t *testing.T) {
	valid := []string{
		"https://agent.internal", "https://agent.internal:8484",
		"http://localhost:8484", "http://127.0.0.1:8484", "http://[::1]:8484",
	}
	for _, u := range valid {
		if _, err := Load(tokenEnv(map[string]string{"HM_AGENT_URL": u})); err != nil {
			t.Errorf("HM_AGENT_URL %q refused: %v", u, err)
		}
	}
	invalid := map[string]string{
		"http remote host":    "http://agent.internal:8484",
		"http private ip":     "http://10.0.0.5:8484",
		"scheme ftp":          "ftp://agent.internal",
		"no scheme":           "agent.internal:8484",
		"no host":             "https://",
		"path":                "https://agent.internal/api",
		"trailing slash":      "https://agent.internal/",
		"query":               "https://agent.internal?a=b",
		"fragment":            "https://agent.internal#x",
		"userinfo":            "https://user:pw@agent.internal",
		"loopback lookalike":  "http://localhost.example.com",
		"http loopback path":  "http://localhost:8484/x",
		"empty trailing hash": "https://agent.internal#",
		"port zero":           "https://agent.internal:0",
		"port too large":      "https://agent.internal:70000",
		"port not a number":   "https://agent.internal:http",
		"empty port":          "https://agent.internal:",
		"no hostname":         "https://:8484",
	}
	for name, u := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := Load(tokenEnv(map[string]string{"HM_AGENT_URL": u}))
			if err == nil || !strings.Contains(err.Error(), "HM_AGENT_URL") {
				t.Fatalf("HM_AGENT_URL %q: err = %v, want one naming HM_AGENT_URL", u, err)
			}
		})
	}
}

func TestLoad_AgentRunTokenTTLAndCAValidation(t *testing.T) {
	for _, ttl := range []string{"0s", "-1m", "59s", "61m", "24h"} {
		if _, err := Load(tokenEnv(map[string]string{"HM_AGENT_RUN_TOKEN_TTL": ttl})); err == nil ||
			!strings.Contains(err.Error(), "HM_AGENT_RUN_TOKEN_TTL") {
			t.Errorf("ttl %q: err = %v, want one naming HM_AGENT_RUN_TOKEN_TTL", ttl, err)
		}
	}
	for _, ttl := range []string{"1m", "1h"} {
		if _, err := Load(tokenEnv(map[string]string{"HM_AGENT_RUN_TOKEN_TTL": ttl})); err != nil {
			t.Errorf("ttl %q refused: %v", ttl, err)
		}
	}
	if _, err := Load(tokenEnv(map[string]string{"HM_AGENT_CA_PEM": "not a certificate"})); err == nil ||
		!strings.Contains(err.Error(), "HM_AGENT_CA_PEM") {
		t.Errorf("bad PEM: err = %v, want one naming HM_AGENT_CA_PEM", err)
	}
	cfg, err := Load(tokenEnv(map[string]string{"HM_AGENT_CA_PEM": testCertPEM(t)}))
	if err != nil || cfg.AgentCAPEM == "" {
		t.Errorf("valid PEM: cfg.AgentCAPEM set = %v, err = %v", cfg.AgentCAPEM != "", err)
	}
}

// testCertPEM returns a throwaway self-signed certificate in PEM form.
func testCertPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test agent ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
	}, &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test agent ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true},
		&key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestLoad_RetentionAgentRuns(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Retention.AgentRuns != 2160*time.Hour {
		t.Errorf("default Retention.AgentRuns = %v, want 2160h", cfg.Retention.AgentRuns)
	}
	cfg, err = Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_RETENTION_AGENT_RUNS": "48h"}))
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Retention.AgentRuns != 48*time.Hour {
		t.Errorf("Retention.AgentRuns = %v, want 48h", cfg.Retention.AgentRuns)
	}
}

func TestLoad_RetentionAgentRunsRefusesAShortValue(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		valid bool
	}{{"1h", false}, {"47h59m", false}, {"1ns", false}, {"-48h", false}, {"48h", true}, {"0", true}, {"2160h", true}} {
		_, err := Load(envFromMap(map[string]string{"HM_DATABASE_URL": "postgres://example", "HM_RETENTION_AGENT_RUNS": tc.raw}))
		if (err == nil) != tc.valid {
			t.Errorf("HM_RETENTION_AGENT_RUNS=%s: err = %v, want valid = %v", tc.raw, err, tc.valid)
		}
		if err != nil && !strings.Contains(err.Error(), "HM_RETENTION_AGENT_RUNS") {
			t.Errorf("HM_RETENTION_AGENT_RUNS=%s: message %q does not name the variable", tc.raw, err)
		}
	}
}

func TestLoad_AgentIdentityLimitsValidation(t *testing.T) {
	cfg, err := Load(tokenEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentIdentityRatePerSecond != 5 || cfg.AgentIdentityLogSearchMaxGB != 2 ||
		cfg.AgentIdentityLogSearchDailyGB != 20 {
		t.Errorf("defaults = %d, %d, %d; want 5, 2, 20",
			cfg.AgentIdentityRatePerSecond, cfg.AgentIdentityLogSearchMaxGB, cfg.AgentIdentityLogSearchDailyGB)
	}

	cfg, err = Load(tokenEnv(map[string]string{
		"HM_AGENT_IDENTITY_RATE_PER_SECOND": "50", "HM_AGENT_IDENTITY_LOG_SEARCH_MAX_GB": "0",
		"HM_AGENT_IDENTITY_LOG_SEARCH_DAILY_GB": "0",
	}))
	if err != nil {
		t.Fatalf("edge values: %v", err)
	}
	if cfg.AgentIdentityRatePerSecond != 50 || cfg.AgentIdentityLogSearchMaxGB != 0 ||
		cfg.AgentIdentityLogSearchDailyGB != 0 {
		t.Errorf("edge values = %d, %d, %d; want 50, 0, 0",
			cfg.AgentIdentityRatePerSecond, cfg.AgentIdentityLogSearchMaxGB, cfg.AgentIdentityLogSearchDailyGB)
	}
	cfg, err = Load(tokenEnv(map[string]string{"HM_AGENT_IDENTITY_RATE_PER_SECOND": "1"}))
	if err != nil || cfg.AgentIdentityRatePerSecond != 1 {
		t.Errorf("rate 1: cfg %d, err %v; want 1 and no error", cfg.AgentIdentityRatePerSecond, err)
	}

	for name, tc := range map[string]struct{ key, value string }{
		"rate zero":       {"HM_AGENT_IDENTITY_RATE_PER_SECOND", "0"},
		"rate above 50":   {"HM_AGENT_IDENTITY_RATE_PER_SECOND", "51"},
		"rate negative":   {"HM_AGENT_IDENTITY_RATE_PER_SECOND", "-1"},
		"max negative":    {"HM_AGENT_IDENTITY_LOG_SEARCH_MAX_GB", "-1"},
		"daily negative":  {"HM_AGENT_IDENTITY_LOG_SEARCH_DAILY_GB", "-1"},
		"max too large":   {"HM_AGENT_IDENTITY_LOG_SEARCH_MAX_GB", "1000001"},
		"daily too large": {"HM_AGENT_IDENTITY_LOG_SEARCH_DAILY_GB", "1000001"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(tokenEnv(map[string]string{tc.key: tc.value}))
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("err = %v, want it to name %s", err, tc.key)
			}
		})
	}
}
