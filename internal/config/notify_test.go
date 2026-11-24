package config

import (
	"net/netip"
	"strings"
	"testing"
	"time"
)

func loadWith(extra map[string]string) (Config, error) {
	env := map[string]string{"HM_DATABASE_URL": "postgres://example"}
	for k, v := range extra {
		env[k] = v
	}
	return Load(envFromMap(env))
}

func TestConfig_NotifyDefaults(t *testing.T) {
	cfg, err := loadWith(nil)
	if err != nil {
		t.Fatal(err)
	}
	n := cfg.Notify
	if n.Interval != 10*time.Second || !n.FakeDemo || len(n.AllowedPrivateCIDRs) != 0 {
		t.Errorf("notify defaults = %+v", n)
	}
	if n.Settle != 3*time.Minute {
		t.Errorf("Settle = %v, want 3m", n.Settle)
	}
	if n.SMTP.Enabled() || n.SMTP.Port != 587 || n.SMTP.TLS != "starttls" {
		t.Errorf("smtp defaults = %+v", n.SMTP)
	}
}

func TestConfig_NotifyIntervalMinimum(t *testing.T) {
	if _, err := loadWith(map[string]string{"HM_NOTIFY_INTERVAL": "1s"}); err != nil {
		t.Fatalf("1s edge: %v", err)
	}
	_, err := loadWith(map[string]string{"HM_NOTIFY_INTERVAL": "500ms"})
	if err == nil || !strings.Contains(err.Error(), "HM_NOTIFY_INTERVAL") {
		t.Fatalf("err = %v, want one naming HM_NOTIFY_INTERVAL", err)
	}
}

func TestConfig_AllowedPrivateCIDRs(t *testing.T) {
	cfg, err := loadWith(map[string]string{"HM_NOTIFY_ALLOWED_PRIVATE_CIDRS": "10.1.0.0/16, fd12::/48"})
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16"), netip.MustParsePrefix("fd12::/48")}
	if len(cfg.Notify.AllowedPrivateCIDRs) != 2 || cfg.Notify.AllowedPrivateCIDRs[0] != want[0] || cfg.Notify.AllowedPrivateCIDRs[1] != want[1] {
		t.Fatalf("cidrs = %v, want %v", cfg.Notify.AllowedPrivateCIDRs, want)
	}
	for _, bad := range []string{"8.8.8.0/24", "169.254.0.0/16", "garbage", "10.0.0.0/7"} {
		_, err := loadWith(map[string]string{"HM_NOTIFY_ALLOWED_PRIVATE_CIDRS": bad})
		if err == nil || !strings.Contains(err.Error(), "HM_NOTIFY_ALLOWED_PRIVATE_CIDRS") {
			t.Errorf("%q: err = %v, want one naming HM_NOTIFY_ALLOWED_PRIVATE_CIDRS", bad, err)
		}
	}
	_, err = loadWith(map[string]string{"HM_NOTIFY_ALLOWED_PRIVATE_CIDRS": "8.8.8.0/24"})
	if err == nil || !strings.Contains(err.Error(), "8.8.8.0/24 is not a private range") {
		t.Errorf("err = %v, want 'is not a private range'", err)
	}
}

func TestConfig_SMTPValidation(t *testing.T) {
	base := map[string]string{"HM_SMTP_HOST": "mail.example.com", "HM_SMTP_FROM": "Harbormaster <hm@example.com>"}
	with := func(over map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range over {
			m[k] = v
		}
		return m
	}
	cfg, err := loadWith(with(map[string]string{"HM_SMTP_USERNAME": "u", "HM_SMTP_PASSWORD": "p", "HM_SMTP_TLS": "implicit", "HM_SMTP_PORT": "465"}))
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Notify.SMTP
	if !s.Enabled() || s.Host != "mail.example.com" || s.Port != 465 || s.TLS != "implicit" || s.Username != "u" || s.Password != "p" {
		t.Errorf("smtp = %+v", s)
	}

	for name, tc := range map[string]struct {
		env  map[string]string
		want string
	}{
		"missing from":      {map[string]string{"HM_SMTP_FROM": ""}, "HM_SMTP_FROM"},
		"unparsable from":   {map[string]string{"HM_SMTP_FROM": "not an address"}, "HM_SMTP_FROM"},
		"port zero":         {map[string]string{"HM_SMTP_PORT": "0"}, "HM_SMTP_PORT"},
		"port too large":    {map[string]string{"HM_SMTP_PORT": "70000"}, "HM_SMTP_PORT"},
		"unknown tls":       {map[string]string{"HM_SMTP_TLS": "plain"}, "HM_SMTP_TLS"},
		"username only":     {map[string]string{"HM_SMTP_USERNAME": "u"}, "HM_SMTP_PASSWORD"},
		"password only":     {map[string]string{"HM_SMTP_PASSWORD": "p"}, "HM_SMTP_USERNAME"},
		"password not echo": {map[string]string{"HM_SMTP_PASSWORD": "hunter2-secret"}, "HM_SMTP_USERNAME"},
	} {
		_, err := loadWith(with(tc.env))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want one naming %s", name, err, tc.want)
		}
		if err != nil && strings.Contains(err.Error(), "hunter2-secret") {
			t.Errorf("%s: error echoes the password", name)
		}
	}

	// With the host unset every other SMTP value is ignored.
	if _, err := loadWith(map[string]string{"HM_SMTP_PORT": "0", "HM_SMTP_TLS": "plain", "HM_SMTP_FROM": "junk", "HM_SMTP_USERNAME": "u"}); err != nil {
		t.Errorf("host unset should ignore the rest: %v", err)
	}
}

func TestConfig_NotificationLogRetentionDefault(t *testing.T) {
	cfg, err := loadWith(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.NotificationLog != 720*time.Hour {
		t.Errorf("NotificationLog = %v, want 720h", cfg.Retention.NotificationLog)
	}
}

func TestConfig_NotifySettleBounds(t *testing.T) {
	for in, want := range map[string]time.Duration{"0s": 0, "90s": 90 * time.Second, "30m": 30 * time.Minute} {
		cfg, err := loadWith(map[string]string{"HM_NOTIFY_SETTLE": in})
		if err != nil || cfg.Notify.Settle != want {
			t.Errorf("%s: settle = %v err=%v, want %v", in, cfg.Notify.Settle, err, want)
		}
	}
	for _, bad := range []string{"-1s", "31m"} {
		_, err := loadWith(map[string]string{"HM_NOTIFY_SETTLE": bad})
		if err == nil || !strings.Contains(err.Error(), "HM_NOTIFY_SETTLE") {
			t.Errorf("%s: err = %v, want one naming HM_NOTIFY_SETTLE", bad, err)
		}
	}
}

func TestConfig_AgentConversationRetention(t *testing.T) {
	cfg, err := loadWith(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Retention.AgentConversations != 720*time.Hour {
		t.Errorf("AgentConversations = %v, want 720h", cfg.Retention.AgentConversations)
	}
	cfg, err = loadWith(map[string]string{"HM_RETENTION_AGENT_CONVERSATIONS": "24h"})
	if err != nil || cfg.Retention.AgentConversations != 24*time.Hour {
		t.Errorf("override = %v, %v; want 24h", cfg.Retention.AgentConversations, err)
	}
}
