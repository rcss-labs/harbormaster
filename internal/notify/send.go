package notify

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"time"

	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/safedial"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// Result is the outcome of one send attempt.
type Result struct {
	Delivered, Retryable bool
	StatusCode           int
	RetryAfter           time.Duration
	Duration             time.Duration
	// Note is user-facing. It never contains the URL, a secret, or text from the response.
	Note string
}

// Senders sends messages to every destination. The unexported fields are test hooks, as in the scanner.
type Senders struct {
	BaseURL, Version string
	// Guard is the outbound policy: public addresses only, plus the deployer's private CIDRs. AllowLoopback is
	// set only in fake mode.
	Guard   safedial.Policy
	SMTP    config.SMTPConfig
	Timeout time.Duration // per attempt, default 10 s
	// FakeBase is "{BaseURL}/fake-notify" in fake mode and empty otherwise. URLs under it may use http, and
	// PagerDuty posts there instead of to PagerDuty.
	FakeBase string
	// TelegramBase is the Bot API host, default "https://api.telegram.org"; "{FakeBase}/telegram" in fake mode.
	TelegramBase string
	// FakeMail records an email instead of speaking SMTP (fake mode).
	FakeMail func(from string, to []string, msg []byte) error
	// Log receives the class of a transport failure and nothing else: request URLs hold secrets.
	Log *slog.Logger

	lookup func(ctx context.Context, host string) ([]netip.Addr, error)
	dial   func(ctx context.Context, network, address string) (net.Conn, error)
	tls    *tls.Config
}

// destination is one entry of the dispatch table: how to send to it and how to validate what an admin entered.
type destination struct {
	send     func(s *Senders, ctx context.Context, r store.NotificationRoute, secret Secret, m Message) Result
	validate func(s *Senders, cfg store.NotificationConfig, secret Secret) error
}

// destinations is the dispatch table; adding a destination is one entry here and one file.
var destinations = map[string]destination{
	DestSlack:     {(*Senders).sendSlack, (*Senders).validateSlack},
	DestPagerDuty: {(*Senders).sendPagerDuty, (*Senders).validatePagerDuty},
	DestWebhook:   {(*Senders).sendWebhook, (*Senders).validateWebhook},
	DestEmail:     {(*Senders).sendEmail, (*Senders).validateEmail},
}

func (s *Senders) timeout() time.Duration {
	if s.Timeout > 0 {
		return s.Timeout
	}
	return 10 * time.Second
}

// Send delivers m to the route's destination once.
func (s *Senders) Send(ctx context.Context, r store.NotificationRoute, secret Secret, m Message) Result {
	d, ok := destinations[r.Destination]
	if !ok {
		return Result{Note: "Unknown destination."}
	}
	return d.send(s, ctx, r, secret, m)
}

// ValidateDestination checks what an admin entered, without any network call. The error is user-facing.
func (s *Senders) ValidateDestination(destination string, cfg store.NotificationConfig, secret Secret) error {
	d, ok := destinations[destination]
	if !ok {
		return errors.New("Unknown destination.")
	}
	return d.validate(s, cfg, secret)
}

// EmailAvailable reports whether email routes can be saved and sent.
func (s *Senders) EmailAvailable() bool { return s.SMTP.Enabled() || s.FakeMail != nil }
