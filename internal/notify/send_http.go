package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rcss-labs/harbormaster/internal/safedial"
	"github.com/rcss-labs/harbormaster/internal/store"
)

const (
	notReachable    = "The destination is not reachable from Harbormaster."
	maxRetryAfter   = 5 * time.Minute
	maxReplyBytes   = 4 << 10
	maxWebhookURL   = 2000
	pagerDutyUSURL  = "https://events.pagerduty.com/v2/enqueue"
	pagerDutyEUURL  = "https://events.eu.pagerduty.com/v2/enqueue"
	maxEmailRecipts = 10
)

// httpReply is a response, reduced to what the destinations read: the status, the headers and the first 4 KiB.
type httpReply struct {
	status   int
	header   http.Header
	body     []byte
	duration time.Duration
}

// underFake reports whether rawURL is under FakeBase, the only place plain http is allowed.
func (s *Senders) underFake(rawURL string) bool {
	return s.FakeBase != "" && strings.HasPrefix(rawURL, s.FakeBase+"/")
}

// postJSON posts body to rawURL through the guarded client: no proxy, no redirects, address checked at dial time.
// On a transport failure it returns a finished Result and false. The request URL is a secret for several
// destinations (and carries the bot token for Telegram), so an error from the client is classified and dropped,
// never put in a note, a returned error or a log line.
func (s *Senders) postJSON(ctx context.Context, rawURL string, body []byte, m Message, extra map[string]string) (httpReply, Result, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "https" && !s.underFake(rawURL)) {
		return httpReply{}, Result{Note: notReachable}, false
	}
	var tlsConfig *tls.Config
	if s.tls != nil {
		tlsConfig = s.tls.Clone()
	}
	dialer := &safedial.Dialer{Policy: s.Guard, Lookup: s.lookup, Dial: s.dial}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			TLSClientConfig:     tlsConfig,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableKeepAlives:   true,
		},
		Timeout:       s.timeout(),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return httpReply{}, Result{Note: notReachable}, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Harbormaster/"+s.Version)
	req.Header.Set("X-Harbormaster-Event", string(m.Event))
	req.Header.Set("X-Harbormaster-Delivery", strconv.FormatInt(m.DeliveryID, 10))
	for name, value := range extra {
		req.Header.Set(name, value)
	}

	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		res := s.transportResult(err)
		res.Duration = elapsed
		return httpReply{}, res, false
	}
	defer resp.Body.Close()
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	return httpReply{status: resp.StatusCode, header: resp.Header, body: reply, duration: elapsed}, Result{}, true
}

// transportResult classifies an error from a client or a mail exchange. Only the class is logged.
func (s *Senders) transportResult(err error) Result {
	var res Result
	var class string
	var netErr net.Error
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	var recordHeader tls.RecordHeaderError
	var alert tls.AlertError
	switch {
	case errors.Is(err, safedial.ErrBlocked):
		class, res = "blocked", Result{Note: notReachable}
	case errors.As(err, &unknownAuthority), errors.As(err, &hostname), errors.As(err, &invalid),
		errors.As(err, &verification), errors.As(err, &recordHeader), errors.As(err, &alert):
		class, res = "tls", Result{Note: "TLS error"}
	case errors.Is(err, context.Canceled):
		class, res = "canceled", Result{Retryable: true, Note: "Cancelled"} // a shutdown, not the destination's fault
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		class, res = "timeout", Result{Retryable: true, Note: fmt.Sprintf("Timed out after %d s", int(s.timeout().Seconds()))}
	default:
		class, res = "network", Result{Retryable: true, Note: "Network error"}
	}
	if s.Log != nil {
		s.Log.Warn("notification send failed", "class", class)
	}
	return res
}

// statusResult maps a response to a Result for destinations that answer with a plain status.
func statusResult(r httpReply, now time.Time) Result {
	res := Result{StatusCode: r.status, Duration: r.duration}
	switch {
	case r.status >= 200 && r.status < 300:
		res.Delivered = true
		res.Note = fmt.Sprintf("Delivered in %d ms", r.duration.Milliseconds())
		return res
	case r.status == http.StatusRequestTimeout, r.status == http.StatusTooManyRequests, r.status >= 500:
		res.Retryable = true
		res.RetryAfter = parseRetryAfter(r.header.Get("Retry-After"), now)
	}
	res.Note = fmt.Sprintf("HTTP %d", r.status)
	return res
}

// parseRetryAfter reads seconds or an HTTP date and caps the wait at 5 minutes; anything unusable is zero.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var wait time.Duration
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		wait = time.Duration(seconds) * time.Second
		if seconds > int(maxRetryAfter.Seconds()) {
			wait = maxRetryAfter
		}
	} else if at, err := http.ParseTime(value); err == nil {
		wait = at.Sub(now)
	}
	if wait < 0 {
		return 0
	}
	return min(wait, maxRetryAfter)
}

// post is the shared path of the JSON-over-HTTP destinations.
func (s *Senders) post(ctx context.Context, rawURL string, body []byte, m Message, extra map[string]string) Result {
	reply, res, ok := s.postJSON(ctx, rawURL, body, m, extra)
	if !ok {
		return res
	}
	return statusResult(reply, time.Now())
}

func (s *Senders) sendSlack(ctx context.Context, _ store.NotificationRoute, secret Secret, m Message) Result {
	body, err := SlackBody(m)
	if err != nil {
		return Result{Note: "The message could not be built."}
	}
	return s.post(ctx, secret.URL, body, m, nil)
}

func (s *Senders) sendWebhook(ctx context.Context, _ store.NotificationRoute, secret Secret, m Message) Result {
	body, err := WebhookBody(m)
	if err != nil {
		return Result{Note: "The message could not be built."}
	}
	var extra map[string]string
	if secret.SigningSecret != "" {
		extra = map[string]string{"X-Harbormaster-Signature": Sign(body, secret.SigningSecret)}
	}
	return s.post(ctx, secret.URL, body, m, extra)
}

func (s *Senders) sendPagerDuty(ctx context.Context, r store.NotificationRoute, secret Secret, m Message) Result {
	body, err := PagerDutyBody(m, secret.RoutingKey)
	if err != nil {
		return Result{Note: "The message could not be built."}
	}
	target := pagerDutyUSURL
	switch {
	case s.FakeBase != "":
		target = s.FakeBase + "/pagerduty"
	case r.Config.PagerDutyRegion == "eu":
		target = pagerDutyEUURL
	}
	return s.post(ctx, target, body, m, nil)
}

var (
	pagerDutyKey    = regexp.MustCompile(`^[0-9a-zA-Z]{32}$`)
	errSlackURL     = errors.New("Enter a Slack incoming webhook URL (https://hooks.slack.com/...).")
	errWebhookURL   = errors.New("Enter an https URL.")
	errURLUserinfo  = errors.New("The URL must not contain a username or password.")
	errPagerDutyKey = errors.New("Enter the 32-character integration key from PagerDuty.")
)

func (s *Senders) validateSlack(_ store.NotificationConfig, secret Secret) error {
	if s.underFake(secret.URL) {
		return nil
	}
	u, err := url.Parse(secret.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Hostname() != "hooks.slack.com" && u.Hostname() != "hooks.slack-gov.com") {
		return errSlackURL
	}
	return nil
}

func (s *Senders) validateWebhook(_ store.NotificationConfig, secret Secret) error {
	if s.underFake(secret.URL) {
		return nil
	}
	u, err := url.Parse(secret.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.Fragment != "" || len(secret.URL) > maxWebhookURL {
		return errWebhookURL
	}
	if u.User != nil {
		return errURLUserinfo
	}
	return nil
}

func (s *Senders) validatePagerDuty(cfg store.NotificationConfig, secret Secret) error {
	if !pagerDutyKey.MatchString(secret.RoutingKey) {
		return errPagerDutyKey
	}
	if cfg.PagerDutyRegion != "us" && cfg.PagerDutyRegion != "eu" {
		return errors.New("Choose a PagerDuty region.")
	}
	return nil
}

func (s *Senders) validateEmail(cfg store.NotificationConfig, _ Secret) error {
	if !s.EmailAvailable() {
		return errors.New("Set HM_SMTP_HOST to send email.")
	}
	if len(cfg.Recipients) == 0 {
		return errors.New("Enter at least one email address.")
	}
	if len(cfg.Recipients) > maxEmailRecipts {
		return errors.New("Enter up to 10 email addresses.")
	}
	for _, recipient := range cfg.Recipients {
		if _, err := mail.ParseAddress(recipient); err != nil {
			return errors.New("Enter valid email addresses.")
		}
	}
	return nil
}
