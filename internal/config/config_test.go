package config

import (
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
		},
		Retention: RetentionConfig{
			StoppedTasks:      168 * time.Hour,
			ServiceEvents:     720 * time.Hour,
			CountsHistory:     168 * time.Hour,
			OperationLog:      2160 * time.Hour,
			DeploymentHistory: 2160 * time.Hour,
			AuditLog:          8760 * time.Hour,
			OrphanTags:        720 * time.Hour,
		},
		PageSizeDefault: 100,
		PageSizeMax:     500,
		MaxDesiredCount: 50,
	}

	if cfg != want {
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
