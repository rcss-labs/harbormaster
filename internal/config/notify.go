package config

import (
	"errors"
	"fmt"
	"net/mail"
	"net/netip"
	"time"
)

// SMTPConfig is the outgoing mail server for email notifications (HM_SMTP_*).
type SMTPConfig struct {
	Host, Username, Password, From string
	Port                           int
	TLS                            string // "starttls" | "implicit"
}

// Enabled reports whether email can be sent.
func (c SMTPConfig) Enabled() bool { return c.Host != "" }

// NotifyConfig holds the alert notification settings.
type NotifyConfig struct {
	// Interval is how often the leader plans and sends notifications (HM_NOTIFY_INTERVAL).
	Interval time.Duration
	// AllowedPrivateCIDRs are private ranges notification destinations may reach (HM_NOTIFY_ALLOWED_PRIVATE_CIDRS).
	AllowedPrivateCIDRs []netip.Prefix
	// FakeDemo seeds demo routes in fake AWS mode (HM_FAKE_NOTIFY_DEMO).
	FakeDemo bool
	SMTP     SMTPConfig
}

// privateRanges bounds HM_NOTIFY_ALLOWED_PRIVATE_CIDRS: an operator may open private space, never public or
// link-local space.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

func readNotify(r *envReader) NotifyConfig {
	n := NotifyConfig{
		Interval: r.duration("HM_NOTIFY_INTERVAL", 10*time.Second),
		FakeDemo: r.boolean("HM_FAKE_NOTIFY_DEMO", true),
		SMTP: SMTPConfig{
			Host:     r.text("HM_SMTP_HOST", ""),
			Port:     r.integer("HM_SMTP_PORT", 587),
			Username: r.text("HM_SMTP_USERNAME", ""),
			Password: r.text("HM_SMTP_PASSWORD", ""),
			From:     r.text("HM_SMTP_FROM", ""),
			TLS:      r.text("HM_SMTP_TLS", "starttls"),
		},
	}
	for _, raw := range r.list("HM_NOTIFY_ALLOWED_PRIVATE_CIDRS") {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			r.fail("HM_NOTIFY_ALLOWED_PRIVATE_CIDRS", "%s is not a CIDR range", raw)
			continue
		}
		prefix = prefix.Masked()
		inside := false
		for _, private := range privateRanges {
			if private.Bits() <= prefix.Bits() && private.Contains(prefix.Addr()) {
				inside = true
			}
		}
		if !inside {
			r.fail("HM_NOTIFY_ALLOWED_PRIVATE_CIDRS", "%s is not a private range", raw)
			continue
		}
		n.AllowedPrivateCIDRs = append(n.AllowedPrivateCIDRs, prefix)
	}
	return n
}

// validateNotify never puts the SMTP password in an error.
func validateNotify(n NotifyConfig) error {
	if n.Interval < time.Second {
		return errors.New("HM_NOTIFY_INTERVAL: must be at least 1s")
	}
	s := n.SMTP
	if !s.Enabled() {
		return nil
	}
	if s.From == "" {
		return errors.New("HM_SMTP_FROM: must be set when HM_SMTP_HOST is set")
	}
	if _, err := mail.ParseAddress(s.From); err != nil {
		return fmt.Errorf("HM_SMTP_FROM: %q is not a valid address", s.From)
	}
	if s.Port < 1 || s.Port > 65535 {
		return errors.New("HM_SMTP_PORT: must be between 1 and 65535")
	}
	if s.TLS != "starttls" && s.TLS != "implicit" {
		return errors.New("HM_SMTP_TLS: must be starttls or implicit")
	}
	if s.Username != "" && s.Password == "" {
		return errors.New("HM_SMTP_PASSWORD: must be set when HM_SMTP_USERNAME is set")
	}
	if s.Password != "" && s.Username == "" {
		return errors.New("HM_SMTP_USERNAME: must be set when HM_SMTP_PASSWORD is set")
	}
	return nil
}
