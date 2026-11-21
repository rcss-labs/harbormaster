package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/safedial"
	"github.com/rcss-labs/harbormaster/internal/store"
)

// publicTestAddr is the address every test hostname "resolves" to; the dial hook sends the connection to the
// httptest server instead, so no test reaches the network.
const publicTestAddr = "93.184.216.34"

type recordedRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

type harness struct {
	t       *testing.T
	srv     *httptest.Server
	senders *Senders
	logs    *bytes.Buffer

	mu       sync.Mutex
	requests []recordedRequest
	hosts    []string // names the sender asked the resolver for
	dialled  []string
}

func (h *harness) recorded() []recordedRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]recordedRequest(nil), h.requests...)
}

func newHarness(t *testing.T, handler http.HandlerFunc) *harness {
	t.Helper()
	h := &harness{t: t, logs: &bytes.Buffer{}}
	h.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		h.mu.Lock()
		h.requests = append(h.requests, recordedRequest{r.Method, r.URL.Path, r.Header.Clone(), body})
		h.mu.Unlock()
		if handler != nil {
			handler(w, r)
		}
	}))
	t.Cleanup(h.srv.Close)
	pool := x509.NewCertPool()
	pool.AddCert(h.srv.Certificate())
	h.senders = &Senders{
		BaseURL: "https://hm.example.com", Version: "test", Timeout: 2 * time.Second,
		Log: slog.New(slog.NewTextHandler(h.logs, nil)),
		lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
			h.mu.Lock()
			h.hosts = append(h.hosts, host)
			h.mu.Unlock()
			return []netip.Addr{netip.MustParseAddr(publicTestAddr)}, nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			h.mu.Lock()
			h.dialled = append(h.dialled, address)
			h.mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, h.srv.Listener.Addr().String())
		},
		tls: &tls.Config{RootCAs: pool, ServerName: "example.com"}, // the httptest certificate's name
	}
	return h
}

func (h *harness) send(route store.NotificationRoute, secret Secret, event Event) Result {
	h.t.Helper()
	return h.senders.Send(context.Background(), route, secret, testMessage(event))
}

func webhookRoute() store.NotificationRoute { return store.NotificationRoute{Destination: DestWebhook} }

func TestHTTPSender_DeliversSlackWebhookAndPagerDuty(t *testing.T) {
	h := newHarness(t, nil)

	res := h.send(store.NotificationRoute{Destination: DestSlack}, Secret{URL: "https://example.com/services/T0/B0/TOKEN"}, EventOpened)
	if !res.Delivered || !strings.HasPrefix(res.Note, "Delivered in ") || !strings.HasSuffix(res.Note, " ms") {
		t.Fatalf("slack result = %+v", res)
	}
	got := h.recorded()[0]
	if got.method != "POST" || got.path != "/services/T0/B0/TOKEN" || got.header.Get("Content-Type") != "application/json" ||
		got.header.Get("User-Agent") != "Harbormaster/test" || got.header.Get("X-Harbormaster-Event") != "opened" || got.header.Get("X-Harbormaster-Delivery") != "42" {
		t.Errorf("slack request = %+v", got)
	}
	if want, _ := SlackBody(testMessage(EventOpened)); !bytes.Equal(got.body, want) {
		t.Errorf("slack body = %s", got.body)
	}

	if res := h.send(store.NotificationRoute{Destination: DestPagerDuty}, Secret{RoutingKey: strings.Repeat("a", 32)}, EventOpened); !res.Delivered {
		t.Fatalf("pagerduty result = %+v", res)
	}
	if res := h.send(store.NotificationRoute{Destination: DestPagerDuty, Config: store.NotificationConfig{PagerDutyRegion: "eu"}}, Secret{RoutingKey: strings.Repeat("a", 32)}, EventResolved); !res.Delivered {
		t.Fatalf("pagerduty eu result = %+v", res)
	}
	reqs := h.recorded()
	if reqs[1].path != "/v2/enqueue" || reqs[2].path != "/v2/enqueue" {
		t.Errorf("pagerduty paths = %q, %q", reqs[1].path, reqs[2].path)
	}
	h.mu.Lock()
	hosts := h.hosts
	h.mu.Unlock()
	if hosts[1] != "events.pagerduty.com" || hosts[2] != "events.eu.pagerduty.com" {
		t.Errorf("pagerduty hosts = %v, want the US then the EU host", hosts)
	}

	if res := h.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventRepeat); !res.Delivered {
		t.Fatalf("webhook result = %+v", res)
	}
	if want, _ := WebhookBody(testMessage(EventRepeat)); !bytes.Equal(h.recorded()[3].body, want) {
		t.Errorf("webhook body = %s", h.recorded()[3].body)
	}
}

func TestHTTPSender_SignsWebhookBody(t *testing.T) {
	h := newHarness(t, nil)
	h.send(webhookRoute(), Secret{URL: "https://example.com/hook", SigningSecret: "s3cret"}, EventOpened)
	h.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventOpened)
	reqs := h.recorded()
	if got, want := reqs[0].header.Get("X-Harbormaster-Signature"), Sign(reqs[0].body, "s3cret"); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
	if reqs[1].header.Get("X-Harbormaster-Signature") != "" {
		t.Error("an unsigned route must not send a signature header")
	}
}

func TestHTTPSender_StatusMapping(t *testing.T) {
	tests := []struct {
		status    int
		retryable bool
		delivered bool
		retry     time.Duration
		note      string
	}{
		{200, false, true, 0, "Delivered in"},
		{202, false, true, 0, "Delivered in"},
		{400, false, false, 0, "HTTP 400"},
		{404, false, false, 0, "HTTP 404"},
		{408, true, false, 0, "HTTP 408"},
		{429, true, false, 120 * time.Second, "HTTP 429"},
		{500, true, false, 0, "HTTP 500"},
		{503, true, false, 0, "HTTP 503"},
		{302, false, false, 0, "HTTP 302"},
	}
	for _, tc := range tests {
		h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
			if tc.status == 429 {
				w.Header().Set("Retry-After", "120")
			}
			w.WriteHeader(tc.status)
		})
		res := h.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventOpened)
		if res.Delivered != tc.delivered || res.Retryable != tc.retryable || res.RetryAfter != tc.retry || !strings.HasPrefix(res.Note, tc.note) || res.StatusCode != tc.status {
			t.Errorf("status %d: result = %+v", tc.status, res)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"30": 30 * time.Second, "99999": 5 * time.Minute, "": 0, "garbage": 0, "-5": 0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Hour).Format(http.TimeFormat):       0,
	} {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestHTTPSender_DoesNotFollowRedirects(t *testing.T) {
	second := newHarness(t, nil)
	first := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.srv.URL+"/landing", http.StatusFound)
	})
	res := first.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventOpened)
	if res.Delivered || res.Retryable || res.Note != "HTTP 302" {
		t.Fatalf("result = %+v, want a plain HTTP 302 failure", res)
	}
	if n := len(second.recorded()); n != 0 {
		t.Fatalf("the redirect target saw %d requests, want none", n)
	}
}

func TestHTTPSender_BlockedDestinationIsNotDialed(t *testing.T) {
	for _, addr := range []string{"169.254.169.254", "10.0.0.5", "127.0.0.1"} {
		h := newHarness(t, nil)
		h.senders.lookup = func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(addr)}, nil
		}
		res := h.send(webhookRoute(), Secret{URL: "https://example.com/hook/SECRETTOKEN"}, EventOpened)
		if res.Delivered || res.Retryable || res.Note != "The destination is not reachable from Harbormaster." {
			t.Errorf("%s: result = %+v", addr, res)
		}
		if len(h.dialled) != 0 {
			t.Errorf("%s: the dial hook was called: %v", addr, h.dialled)
		}
	}

	// A deployer allow-list opens its private range only.
	h := newHarness(t, nil)
	h.senders.Guard = safedial.Policy{AllowedPrivate: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	h.senders.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.0.0.5")}, nil
	}
	if res := h.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventOpened); !res.Delivered {
		t.Fatalf("allow-listed private destination: %+v", res)
	}
	if len(h.dialled) != 1 || h.dialled[0] != "10.0.0.5:443" {
		t.Errorf("dialled %v, want 10.0.0.5:443", h.dialled)
	}
}

func TestHTTPSender_TimeoutIsRetryable(t *testing.T) {
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	})
	h.senders.Timeout = 100 * time.Millisecond
	res := h.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventOpened)
	if res.Delivered || !res.Retryable || !strings.HasPrefix(res.Note, "Timed out after ") {
		t.Fatalf("result = %+v", res)
	}
}

func TestHTTPSender_NoteNeverEchoesURLOrBody(t *testing.T) {
	const marker, token, routingKey = "BODYMARKER", "URLPATHTOKEN", "ROUTINGKEY0123456789abcdefghij"
	h := newHarness(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, marker+" "+token+" "+routingKey)
	})
	for name, res := range map[string]Result{
		"webhook":   h.send(webhookRoute(), Secret{URL: "https://example.com/" + token}, EventOpened),
		"slack":     h.send(store.NotificationRoute{Destination: DestSlack}, Secret{URL: "https://example.com/" + token}, EventOpened),
		"pagerduty": h.send(store.NotificationRoute{Destination: DestPagerDuty}, Secret{RoutingKey: routingKey}, EventOpened),
	} {
		for _, secret := range []string{marker, token, routingKey, "example.com"} {
			if strings.Contains(res.Note, secret) {
				t.Errorf("%s: note %q contains %q", name, res.Note, secret)
			}
		}
		if res.Note != "HTTP 500" {
			t.Errorf("%s: note = %q, want HTTP 500", name, res.Note)
		}
	}
}

// Go's *url.Error text carries the request URL, which for a webhook is a secret. Every transport failure is
// classified, and neither the note nor the log may hold the URL, its path token or the host.
func TestHTTPSender_TransportErrorsAreClassifiedNotEchoed(t *testing.T) {
	const token = "SECRETPATHTOKEN"
	url := "https://secret-host.example/hooks/" + token
	cases := map[string]struct {
		setup     func(h *harness)
		retryable bool
		note      string
	}{
		"connection refused": {func(h *harness) {
			h.senders.dial = func(context.Context, string, string) (net.Conn, error) {
				return nil, &net.OpError{Op: "dial", Net: "tcp", Err: io.ErrClosedPipe}
			}
		}, true, "Network error"},
		"resolver failure": {func(h *harness) {
			h.senders.lookup = func(context.Context, string) ([]netip.Addr, error) {
				return nil, &net.DNSError{Err: "no such host " + url, Name: url}
			}
		}, true, "Network error"},
		"untrusted certificate": {func(h *harness) { h.senders.tls = &tls.Config{ServerName: "example.com"} }, false, "TLS error"},
		"blocked": {func(h *harness) {
			h.senders.lookup = func(context.Context, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
			}
		}, false, "The destination is not reachable from Harbormaster."},
	}
	for name, tc := range cases {
		h := newHarness(t, nil)
		tc.setup(h)
		res := h.send(webhookRoute(), Secret{URL: url}, EventOpened)
		if res.Delivered || res.Retryable != tc.retryable || res.Note != tc.note {
			t.Errorf("%s: result = %+v", name, res)
		}
		for _, leak := range []string{token, "secret-host", "/hooks/"} {
			if strings.Contains(res.Note, leak) || strings.Contains(h.logs.String(), leak) {
				t.Errorf("%s: %q leaked into the note %q or the log %q", name, leak, res.Note, h.logs.String())
			}
		}
	}
}

func TestSenders_LoopbackOnlyInFakeMode(t *testing.T) {
	plain := &Senders{}
	if err := plain.ValidateDestination(DestWebhook, store.NotificationConfig{}, Secret{URL: "http://127.0.0.1/hook"}); err == nil {
		t.Error("an http loopback webhook must be rejected outside fake mode")
	}
	if err := plain.ValidateDestination(DestWebhook, store.NotificationConfig{}, Secret{URL: "https://127.0.0.1/hook"}); err != nil {
		t.Errorf("https validates by shape; the guard blocks loopback at dial time: %v", err)
	}

	// The guard, not the validator, is what blocks a loopback address that gets stored anyway.
	h := newHarness(t, nil)
	h.senders.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	if res := h.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventOpened); res.Note != "The destination is not reachable from Harbormaster." {
		t.Errorf("loopback without AllowLoopback: %+v", res)
	}
	h.senders.Guard.AllowLoopback = true
	if res := h.send(webhookRoute(), Secret{URL: "https://example.com/hook"}, EventOpened); !res.Delivered {
		t.Errorf("loopback with AllowLoopback: %+v", res)
	}

	fake := &Senders{FakeBase: "http://127.0.0.1:8480/fake-notify", Guard: safedial.Policy{AllowLoopback: true}}
	if err := fake.ValidateDestination(DestWebhook, store.NotificationConfig{}, Secret{URL: "http://127.0.0.1:8480/fake-notify/webhook/ok"}); err != nil {
		t.Errorf("a URL under FakeBase must validate in fake mode: %v", err)
	}
	if err := fake.ValidateDestination(DestWebhook, store.NotificationConfig{}, Secret{URL: "http://127.0.0.1:8480/other"}); err == nil {
		t.Error("only URLs under FakeBase may use http")
	}
}

func TestSend_RefusesAPlainHTTPURLOutsideFakeMode(t *testing.T) {
	h := newHarness(t, nil)
	res := h.send(webhookRoute(), Secret{URL: "http://example.com/hook"}, EventOpened)
	if res.Delivered || res.Retryable || len(h.recorded()) != 0 {
		t.Fatalf("result = %+v, requests = %d, want a refusal without a request", res, len(h.recorded()))
	}
}

func TestSend_UnknownDestinationFails(t *testing.T) {
	res := (&Senders{}).Send(context.Background(), store.NotificationRoute{Destination: "pager"}, Secret{}, testMessage(EventOpened))
	if res.Delivered || res.Retryable || res.Note != "Unknown destination." {
		t.Fatalf("result = %+v", res)
	}
}

func TestValidateDestination(t *testing.T) {
	s := &Senders{SMTP: smtpEnabled()}
	key := strings.Repeat("a", 32)
	long := "https://example.com/" + strings.Repeat("a", 2000)
	tests := []struct {
		name        string
		destination string
		cfg         store.NotificationConfig
		secret      Secret
		wantErr     string // "" = valid
	}{
		{"slack ok", DestSlack, store.NotificationConfig{}, Secret{URL: "https://hooks.slack.com/services/T/B/x"}, ""},
		{"slack gov ok", DestSlack, store.NotificationConfig{}, Secret{URL: "https://hooks.slack-gov.com/services/T/B/x"}, ""},
		{"slack http", DestSlack, store.NotificationConfig{}, Secret{URL: "http://hooks.slack.com/services/x"}, "Enter a Slack incoming webhook URL"},
		{"slack other host", DestSlack, store.NotificationConfig{}, Secret{URL: "https://evil.example/services/x"}, "Enter a Slack incoming webhook URL"},
		{"slack lookalike host", DestSlack, store.NotificationConfig{}, Secret{URL: "https://hooks.slack.com.evil.example/x"}, "Enter a Slack incoming webhook URL"},
		{"slack userinfo", DestSlack, store.NotificationConfig{}, Secret{URL: "https://user:pw@hooks.slack.com/x"}, "Enter a Slack incoming webhook URL"},
		{"slack empty", DestSlack, store.NotificationConfig{}, Secret{}, "Enter a Slack incoming webhook URL"},
		{"webhook ok", DestWebhook, store.NotificationConfig{}, Secret{URL: "https://example.com/hook?x=1"}, ""},
		{"webhook http", DestWebhook, store.NotificationConfig{}, Secret{URL: "http://example.com/hook"}, "Enter an https URL."},
		{"webhook no host", DestWebhook, store.NotificationConfig{}, Secret{URL: "https:///hook"}, "Enter an https URL."},
		{"webhook userinfo", DestWebhook, store.NotificationConfig{}, Secret{URL: "https://u:p@example.com/hook"}, "The URL must not contain a username or password."},
		{"webhook fragment", DestWebhook, store.NotificationConfig{}, Secret{URL: "https://example.com/hook#frag"}, "Enter an https URL."},
		{"webhook too long", DestWebhook, store.NotificationConfig{}, Secret{URL: long}, "Enter an https URL."},
		{"pagerduty ok", DestPagerDuty, store.NotificationConfig{PagerDutyRegion: "eu"}, Secret{RoutingKey: key}, ""},
		{"pagerduty short key", DestPagerDuty, store.NotificationConfig{PagerDutyRegion: "us"}, Secret{RoutingKey: "short"}, "32"},
		{"pagerduty bad chars", DestPagerDuty, store.NotificationConfig{PagerDutyRegion: "us"}, Secret{RoutingKey: strings.Repeat("a", 31) + "!"}, "32"},
		{"pagerduty bad region", DestPagerDuty, store.NotificationConfig{PagerDutyRegion: "ap"}, Secret{RoutingKey: key}, "region"},
		{"email ok", DestEmail, store.NotificationConfig{Recipients: []string{"a@example.com", "b@example.com"}}, Secret{}, ""},
		{"email none", DestEmail, store.NotificationConfig{}, Secret{}, "at least one"},
		{"email eleven", DestEmail, store.NotificationConfig{Recipients: strings.Split(strings.Repeat("a@example.com,", 11), ",")[:11]}, Secret{}, "up to 10"},
		{"email malformed", DestEmail, store.NotificationConfig{Recipients: []string{"not an address"}}, Secret{}, "valid email"},
		{"unknown", "pager", store.NotificationConfig{}, Secret{}, "Unknown destination."},
	}
	for _, tc := range tests {
		err := s.ValidateDestination(tc.destination, tc.cfg, tc.secret)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tc.name, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("%s: error = %v, want one containing %q", tc.name, err, tc.wantErr)
		}
	}
	noMail := &Senders{}
	if err := noMail.ValidateDestination(DestEmail, store.NotificationConfig{Recipients: []string{"a@example.com"}}, Secret{}); err == nil || err.Error() != "Set HM_SMTP_HOST to send email." {
		t.Errorf("email without SMTP = %v", err)
	}
	if noMail.EmailAvailable() || !s.EmailAvailable() {
		t.Error("EmailAvailable must follow the SMTP host")
	}
	if !(&Senders{FakeMail: func(string, []string, []byte) error { return nil }}).EmailAvailable() {
		t.Error("fake mode treats email as available")
	}
}
