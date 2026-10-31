// Package config loads Harbormaster settings from HM_* environment variables.
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
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
	LogLevel                string
	SessionIdle             time.Duration
	SessionAbsolute         time.Duration
	LoginMaxPerUser         int
	LoginMaxPerIP           int
	LoginWindow             time.Duration
	Poll                    PollConfig
	Retention               RetentionConfig
	PageSizeDefault         int
	PageSizeMax             int
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
		LogLevel:                reader.text("HM_LOG_LEVEL", "info"),
		SessionIdle:             reader.duration("HM_SESSION_IDLE", 12*time.Hour),
		SessionAbsolute:         reader.duration("HM_SESSION_ABSOLUTE", 168*time.Hour),
		LoginMaxPerUser:         reader.integer("HM_LOGIN_MAX_PER_USER", 5),
		LoginMaxPerIP:           reader.integer("HM_LOGIN_MAX_PER_IP", 30),
		LoginWindow:             reader.duration("HM_LOGIN_WINDOW", 15*time.Minute),
		Poll: PollConfig{
			Services:      reader.duration("HM_POLL_SERVICES", 5*time.Second),
			RunningTasks:  reader.duration("HM_POLL_RUNNING_TASKS", 5*time.Second),
			FullTasks:     reader.duration("HM_POLL_FULL_TASKS", 30*time.Second),
			StoppedTasks:  reader.duration("HM_POLL_STOPPED_TASKS", 15*time.Second),
			Clusters:      reader.duration("HM_POLL_CLUSTERS", 60*time.Second),
			ServiceList:   reader.duration("HM_POLL_SERVICE_LIST", 60*time.Second),
			CountsHistory: reader.duration("HM_POLL_COUNTS_HISTORY", 5*time.Minute),
			MaxInFlight:   reader.integer("HM_POLL_MAX_INFLIGHT", 8),
			Jitter:        reader.duration("HM_POLL_JITTER", 500*time.Millisecond),
		},
		Retention: RetentionConfig{
			StoppedTasks:      reader.duration("HM_RETENTION_STOPPED_TASKS", 168*time.Hour),
			ServiceEvents:     reader.duration("HM_RETENTION_SERVICE_EVENTS", 720*time.Hour),
			CountsHistory:     reader.duration("HM_RETENTION_COUNTS_HISTORY", 168*time.Hour),
			OperationLog:      reader.duration("HM_RETENTION_OPERATION_LOG", 2160*time.Hour),
			DeploymentHistory: reader.duration("HM_RETENTION_DEPLOYMENT_HISTORY", 2160*time.Hour),
			AuditLog:          reader.duration("HM_RETENTION_AUDIT_LOG", 8760*time.Hour),
			OrphanTags:        reader.duration("HM_RETENTION_ORPHAN_TAGS", 720*time.Hour),
		},
		PageSizeDefault: reader.integer("HM_PAGE_SIZE_DEFAULT", 100),
		PageSizeMax:     reader.integer("HM_PAGE_SIZE_MAX", 500),
	}

	if reader.err != nil {
		return Config{}, reader.err
	}
	if cfg.DatabaseURL == "" && cfg.DatabaseSecretARN == "" {
		return Config{}, errors.New("HM_DATABASE_URL or HM_DATABASE_SECRET_ARN must be set")
	}
	return cfg, nil
}
