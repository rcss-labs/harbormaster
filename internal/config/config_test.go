package config

import (
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
			Services:      5 * time.Second,
			RunningTasks:  5 * time.Second,
			FullTasks:     30 * time.Second,
			StoppedTasks:  15 * time.Second,
			Clusters:      60 * time.Second,
			ServiceList:   60 * time.Second,
			CountsHistory: 5 * time.Minute,
			MaxInFlight:   8,
			Jitter:        500 * time.Millisecond,
			TargetHealth:  15 * time.Second,
			Scaling:       60 * time.Second,
			Network:       5 * time.Minute,
			Storage:       5 * time.Minute,
		},
		Retention: RetentionConfig{
			StoppedTasks:      168 * time.Hour,
			ServiceEvents:     720 * time.Hour,
			CountsHistory:     168 * time.Hour,
			OperationLog:      2160 * time.Hour,
			DeploymentHistory: 2160 * time.Hour,
			AuditLog:          8760 * time.Hour,
			OrphanTags:        720 * time.Hour,
			AvailabilityDaily: 8760 * time.Hour,
			CapacityDaily:     8760 * time.Hour,
			UtilDaily:         2160 * time.Hour,
			ScanImages:        168 * time.Hour,
			ScanRuns:          720 * time.Hour,
		},
		Scan: ScanConfig{
			RoleName:         "harbormaster-scan",
			Interval:         60 * time.Second,
			TaskTimeout:      30 * time.Minute,
			DBRefresh:        24 * time.Hour,
			DBMaxAge:         120 * time.Hour,
			CandidateRefresh: 6 * time.Hour,
			MaxTasks:         1,
			BatchSize:        25,
			CandidateTags:    3,
			MaxImageMB:       4096,
			RematchBatch:     500,
		},
		Analytics:       AnalyticsConfig{Util: true},
		PageSizeDefault: 100,
		PageSizeMax:     500,
		MetricsCacheTTL: 60 * time.Second,
		LogTailIdle:     30 * time.Minute,
		LogTailsPerUser: 2,
		LogHistoryLimit: 500,
		MaxDesiredCount: 50,
		SAMLRequestTTL:  10 * time.Minute,
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
		{"Poll.TargetHealth", cfg.Poll.TargetHealth, 15 * time.Second},
		{"Poll.Scaling", cfg.Poll.Scaling, 60 * time.Second},
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
	if cfg.Scan.Interval != 60*time.Second {
		t.Errorf("Scan.Interval = %v, want 60s", cfg.Scan.Interval)
	}
	if cfg.Scan.CandidateTags != 3 {
		t.Errorf("Scan.CandidateTags = %d, want 3", cfg.Scan.CandidateTags)
	}
}

func TestLoad_ScanEnabledNeedsLaunchSettings(t *testing.T) {
	_, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL":         "postgres://example",
		"HM_SCAN_ENABLED":         "true",
		"HM_SCAN_CLUSTER_ARN":     "arn:aws:ecs:us-east-1:123456789012:cluster/scan",
		"HM_SCAN_TASK_DEFINITION": "scanner:1",
		"HM_SCAN_SUBNETS":         "subnet-a",
		"HM_SCAN_SECURITY_GROUP":  "sg-a",
	}))
	if err == nil || !strings.Contains(err.Error(), "HM_SCAN_BUCKET") {
		t.Fatalf("err = %v, want one naming HM_SCAN_BUCKET", err)
	}
}

func TestLoad_ScanEnabledInFakeModeNeedsNothing(t *testing.T) {
	cfg, err := Load(envFromMap(map[string]string{
		"HM_DATABASE_URL": "postgres://example",
		"HM_FAKE_AWS":     "true",
		"HM_SCAN_ENABLED": "true",
	}))
	if err != nil {
		t.Fatalf("Load returned error in fake mode: %v", err)
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
		name string
		key  string
		raw  string
	}{
		{name: "max tasks below range", key: "HM_SCAN_MAX_TASKS", raw: "0"},
		{name: "max tasks above range", key: "HM_SCAN_MAX_TASKS", raw: "5"},
		{name: "batch size below range", key: "HM_SCAN_BATCH_SIZE", raw: "0"},
		{name: "batch size above range", key: "HM_SCAN_BATCH_SIZE", raw: "101"},
		{name: "candidate tags above range", key: "HM_SCAN_CANDIDATE_TAGS", raw: "11"},
		{name: "task timeout below minimum", key: "HM_SCAN_TASK_TIMEOUT", raw: "4m"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(envFromMap(map[string]string{
				"HM_DATABASE_URL": "postgres://example",
				tt.key:            tt.raw,
			}))
			if err == nil || !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("err = %v, want one naming %s", err, tt.key)
			}
		})
	}
}
