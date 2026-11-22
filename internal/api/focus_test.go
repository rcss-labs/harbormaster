package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/awsx/fake"
	"github.com/rcss-labs/harbormaster/internal/config"
	"github.com/rcss-labs/harbormaster/internal/focus"
	"github.com/rcss-labs/harbormaster/internal/model"
	"github.com/rcss-labs/harbormaster/internal/signal"
	"github.com/rcss-labs/harbormaster/internal/store"
	"github.com/rcss-labs/harbormaster/internal/telemetry"
)

// recordingBus keeps what was published on the bus.
type recordingBus struct {
	signal.Bus
	mu        sync.Mutex
	published []string
}

func (b *recordingBus) Publish(ctx context.Context, ch signal.Channel, payload string) error {
	b.mu.Lock()
	b.published = append(b.published, string(ch)+":"+payload)
	b.mu.Unlock()
	return b.Bus.Publish(ctx, ch, payload)
}

func (b *recordingBus) snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.published...)
}

// failingRegistry fails every call, like a registry whose store went away.
type failingRegistry struct{ focus.Registry }

func (failingRegistry) Heartbeat(context.Context, focus.Object, int64, focus.Limits) (focus.Admission, error) {
	return "", errors.New("store down")
}

type storeDown struct{}

func (storeDown) Up() bool { return false }

type focusFixture struct {
	srv *httptest.Server
	fk  *fake.Fake
	st  *store.Store
	reg focus.Registry
	bus *recordingBus
	tel *telemetry.Registry
}

func newFocusFixture(t *testing.T, mutate func(*Server, *focusFixture)) *focusFixture {
	t.Helper()
	f := &focusFixture{reg: focus.NewMemoryRegistry(time.Now), bus: &recordingBus{Bus: signal.NewMemoryBus()}, tel: telemetry.New()}
	srv, _, fk, st, _ := newServerCustomAPI(t, true, func(s *Server) {
		s.Focus, s.Bus, s.Telemetry = f.reg, f.bus, f.tel
		s.Cfg.Poll.Services = 30 * time.Second
		s.Cfg.Focus = config.FocusConfig{Interval: 5 * time.Second, TTL: 30 * time.Second, MaxObjects: 50, MaxPerUser: 5, MaxCallsPerSec: 5}
		if mutate != nil {
			mutate(s, f)
		}
	})
	f.srv, f.fk, f.st = srv, fk, st
	seedMockupTags(t, st)
	pollAll(t, st, fk)
	return f
}

func focusServiceKey(name string) model.ServiceKey {
	return model.ServiceKey{ClusterKey: model.ClusterKey{AccountID: homelabAccountID, Region: "us-east-1", Cluster: "core-services"}, Service: name}
}

func focusServiceObject(name string) focus.Object {
	k := focusServiceKey(name)
	return focus.Object{Kind: focus.KindService, Cluster: k.ClusterKey, Service: k.Service}
}

type focusResponse struct {
	State           string  `json:"state"`
	IntervalSeconds int     `json:"interval_seconds"`
	RefreshedAt     *string `json:"refreshed_at"`
	Message         string  `json:"message"`
	Failed          bool    `json:"failed"`
}

func postFocus(t *testing.T, c *apiClient, kind, id string, open bool) (int, focusResponse) {
	t.Helper()
	resp := c.post("/api/focus", FocusRequest{Kind: kind, ID: id, Open: open})
	status := resp.StatusCode
	var out focusResponse
	if status == http.StatusOK {
		decode(t, resp, &out)
	} else {
		resp.Body.Close()
	}
	return status, out
}

func listed(t *testing.T, reg focus.Registry) []string {
	t.Helper()
	objs, err := reg.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, o := range objs {
		out = append(out, o.Member())
	}
	return out
}

func TestFocus_InvisibleObjectIs404AndRegistersNothing(t *testing.T) {
	f := newFocusFixture(t, nil)
	omer := login(t, f.srv, "omer", testPassword)
	for _, tc := range []struct{ kind, id string }{
		{"service", focusServiceKey("payments-api").ID()},
		{"cluster", focusServiceKey("payments-api").ClusterKey.ID()},
	} {
		if status, _ := postFocus(t, omer, tc.kind, tc.id, true); status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", tc.kind, status)
		}
	}
	if got := listed(t, f.reg); len(got) != 0 {
		t.Errorf("registered %v for a user who may not see the object", got)
	}
	if got := f.bus.snapshot(); len(got) != 0 {
		t.Errorf("published %v for a user who may not see the object", got)
	}
}

func TestFocus_MalformedIDIs404(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	for _, kind := range []string{"service", "cluster"} {
		if status, _ := postFocus(t, sam, kind, "not-an-id", true); status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", kind, status)
		}
	}
	if status, _ := postFocus(t, sam, "service", focusServiceKey("no-such-service").ID(), true); status != http.StatusNotFound {
		t.Errorf("unknown service: status %d, want 404", status)
	}
}

func TestFocus_UnknownKindIs422(t *testing.T) {
	f := newFocusFixture(t, nil)
	if status, _ := postFocus(t, login(t, f.srv, "sam", testPassword), "task", "x", true); status != http.StatusUnprocessableEntity {
		t.Fatalf("status %d, want 422", status)
	}
}

func TestFocus_OpenAdmitsPublishesAndReportsLive(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	obj := focusServiceObject("payments-api")

	status, got := postFocus(t, sam, "service", focusServiceKey("payments-api").ID(), true)
	if status != http.StatusOK || got.State != "live" || got.RefreshedAt != nil {
		t.Fatalf("response = %d %+v, want 200 live with no refreshed_at", status, got)
	}
	if listed := listed(t, f.reg); len(listed) != 1 || listed[0] != obj.Member() {
		t.Fatalf("registry = %v, want %q", listed, obj.Member())
	}
	if pub := f.bus.snapshot(); len(pub) != 1 || pub[0] != "pollnow:"+obj.Member() {
		t.Fatalf("published = %v, want one poll-now for %q", pub, obj.Member())
	}
}

func TestFocus_ClusterObjectsAreSupported(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	k := focusServiceKey("payments-api").ClusterKey
	if status, got := postFocus(t, sam, "cluster", k.ID(), true); status != http.StatusOK || got.State != "live" {
		t.Fatalf("cluster focus = %d %+v", status, got)
	}
	want := focus.Object{Kind: focus.KindCluster, Cluster: k}.Member()
	if got := listed(t, f.reg); len(got) != 1 || got[0] != want {
		t.Fatalf("registry = %v, want %q", got, want)
	}
}

func TestFocus_HeartbeatReportsFreshEvidence(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	postFocus(t, sam, "service", id, true)
	if err := f.reg.SetFresh(context.Background(), focusServiceObject("payments-api"), focus.Fresh{At: time.Now().Add(-time.Second), Interval: 5 * time.Second, State: "live"}); err != nil {
		t.Fatal(err)
	}
	status, got := postFocus(t, sam, "service", id, false)
	if status != http.StatusOK || got.State != "live" || got.IntervalSeconds != 5 || got.RefreshedAt == nil {
		t.Fatalf("response = %d %+v, want live, 5 s, with refreshed_at", status, got)
	}
	if pub := f.bus.snapshot(); len(pub) != 1 {
		t.Fatalf("a heartbeat without open published again: %v", pub)
	}
}

func TestFocus_ThrottledStateAndStretchedInterval(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	postFocus(t, sam, "service", id, true)
	obj := focusServiceObject("payments-api")

	if err := f.reg.SetFresh(context.Background(), obj, focus.Fresh{At: time.Now(), Interval: 10 * time.Second, State: "throttled"}); err != nil {
		t.Fatal(err)
	}
	if _, got := postFocus(t, sam, "service", id, false); got.State != "throttled" || got.IntervalSeconds != 10 {
		t.Fatalf("throttled response = %+v", got)
	}
	if err := f.reg.SetFresh(context.Background(), obj, focus.Fresh{At: time.Now(), Interval: 10 * time.Second, State: "live"}); err != nil {
		t.Fatal(err)
	}
	if _, got := postFocus(t, sam, "service", id, false); got.State != "live" || got.IntervalSeconds != 10 {
		t.Fatalf("stretched response = %+v, want live at 10 s", got)
	}
}

func TestFocus_StaleEvidenceIsNotLive(t *testing.T) {
	clock := &focusTestClock{t: time.Now()}
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.Now = clock.now })
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	postFocus(t, sam, "service", id, true)
	clock.advance(60 * time.Second) // well past the wait for a first read
	// Older than two intervals and at least 10 s: no longer evidence.
	if err := f.reg.SetFresh(context.Background(), focusServiceObject("payments-api"), focus.Fresh{At: clock.now().Add(-40 * time.Second), Interval: 5 * time.Second, State: "live"}); err != nil {
		t.Fatal(err)
	}
	if _, got := postFocus(t, sam, "service", id, false); got.State != "unavailable" {
		t.Fatalf("response with 40 s old evidence = %+v, want unavailable", got)
	}
}

func TestFocus_CappedStillPublishesOnOpen(t *testing.T) {
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.Cfg.Focus.MaxObjects = 1 })
	if _, err := f.reg.Heartbeat(context.Background(), focusServiceObject("web-frontend"), 99, focus.Limits{TTL: time.Minute, MaxObjects: 1, MaxPerUser: 5}); err != nil {
		t.Fatal(err)
	}
	sam := login(t, f.srv, "sam", testPassword)
	status, got := postFocus(t, sam, "service", focusServiceKey("payments-api").ID(), true)
	if status != http.StatusOK || got.State != "capped" || got.IntervalSeconds != 30 || got.Message != "Normal pace (live limit reached)" {
		t.Fatalf("response = %d %+v, want capped at the 30 s services interval", status, got)
	}
	want := "pollnow:" + focusServiceObject("payments-api").Member()
	if pub := f.bus.snapshot(); len(pub) != 1 || pub[0] != want {
		t.Fatalf("published = %v, want the capped page's own immediate read %q", pub, want)
	}
	if got := listed(t, f.reg); len(got) != 1 {
		t.Fatalf("registry = %v, the capped object must not be registered", got)
	}
}

func TestFocus_UserCapped(t *testing.T) {
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.Cfg.Focus.MaxPerUser = 1 })
	sam := login(t, f.srv, "sam", testPassword)
	postFocus(t, sam, "service", focusServiceKey("payments-api").ID(), true)
	status, got := postFocus(t, sam, "service", focusServiceKey("web-frontend").ID(), true)
	if status != http.StatusOK || got.State != "user_capped" || !strings.Contains(got.Message, "1 pages refreshing live") {
		t.Fatalf("response = %d %+v, want user_capped naming the count", status, got)
	}
}

// focusTestClock is a clock the test moves; the server reads it through Server.Now.
type focusTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *focusTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *focusTestClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestFocus_UnavailableWithoutEvidenceLongAfterTheOpen(t *testing.T) {
	clock := &focusTestClock{t: time.Now()}
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.Now = clock.now })
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	if _, got := postFocus(t, sam, "service", id, true); got.State != "live" {
		t.Fatalf("first heartbeat = %+v, want live", got)
	}
	clock.advance(20 * time.Second) // past the wait for the poller's first read
	status, got := postFocus(t, sam, "service", id, false)
	if status != http.StatusOK || got.State != "unavailable" || got.Message != "Live refresh unavailable" || got.Failed {
		t.Fatalf("heartbeat without evidence long after the open = %d %+v, want unavailable and not failed (no evidence is not a fault)", status, got)
	}
}

// The first answer after an open has no evidence yet. A recheck a few seconds later is still waiting for the poller's
// first read, which is not a fault: it must not read as unavailable.
func TestFocus_RecheckSoonAfterAnOpenIsPendingNotUnavailable(t *testing.T) {
	clock := &focusTestClock{t: time.Now()}
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.Now = clock.now })
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	postFocus(t, sam, "service", id, true)
	clock.advance(3 * time.Second)
	status, got := postFocus(t, sam, "service", id, false)
	if status != http.StatusOK || got.State != "live" || got.RefreshedAt != nil || got.Message != "" || got.Failed {
		t.Fatalf("recheck 3 s after an open = %d %+v, want live with no refreshed_at (pending)", status, got)
	}
}

// Opens that publish poll-now are limited per user: a script cannot queue a read cycle per request. The heartbeat itself
// is still admitted and answered; only the signal is skipped, and the poller's registry read starts the loop anyway.
func TestFocus_OpenSignalIsRateLimitedPerUser(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	for i := 0; i < 30; i++ {
		if status, got := postFocus(t, sam, "service", id, true); status != http.StatusOK || got.State == "" {
			t.Fatalf("open %d = %d %+v, want it answered", i, status, got)
		}
	}
	pub := f.bus.snapshot()
	if len(pub) < 10 || len(pub) > 14 {
		t.Fatalf("30 opens in a burst published %d poll-now signals, want the bucket of about 10", len(pub))
	}
	if got := listed(t, f.reg); len(got) != 1 {
		t.Fatalf("registry = %v: limited opens must still register the object", got)
	}
}

func TestFocus_UnavailableWhenStoreIsDown(t *testing.T) {
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.KVHealth = storeDown{} })
	status, got := postFocus(t, login(t, f.srv, "sam", testPassword), "service", focusServiceKey("payments-api").ID(), true)
	if status != http.StatusOK || got.State != "unavailable" || got.Message != "Live refresh unavailable" || !got.Failed {
		t.Fatalf("response = %d %+v, want unavailable and failed", status, got)
	}
	if listed := listed(t, f.reg); len(listed) != 0 {
		t.Fatalf("registered %v while the store is down", listed)
	}
}

func TestFocus_UnavailableWhenRegistryErrors(t *testing.T) {
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) {
		s.Focus = failingRegistry{Registry: focus.NewMemoryRegistry(time.Now)}
	})
	status, got := postFocus(t, login(t, f.srv, "sam", testPassword), "service", focusServiceKey("payments-api").ID(), true)
	if status != http.StatusOK || got.State != "unavailable" || !got.Failed {
		t.Fatalf("response = %d %+v, want unavailable and failed", status, got)
	}
}

func TestFocus_NoRegistryMeansUnavailable(t *testing.T) {
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.Focus = nil })
	status, got := postFocus(t, login(t, f.srv, "sam", testPassword), "service", focusServiceKey("payments-api").ID(), true)
	if status != http.StatusOK || got.State != "unavailable" || !got.Failed {
		t.Fatalf("response = %d %+v, want unavailable and failed", status, got)
	}
}

func TestFocus_DeleteReleasesAndIs204(t *testing.T) {
	f := newFocusFixture(t, func(s *Server, _ *focusFixture) { s.Cfg.Focus.MaxPerUser = 1 })
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	postFocus(t, sam, "service", id, true)

	resp := sam.do(http.MethodDelete, "/api/focus", FocusRequest{Kind: "service", ID: id}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE status %d, want 204", resp.StatusCode)
	}
	if status, got := postFocus(t, sam, "service", focusServiceKey("web-frontend").ID(), true); status != http.StatusOK || got.State == "user_capped" {
		t.Fatalf("after releasing, the user's slot is still taken: %d %+v", status, got)
	}
	// Releasing reveals nothing: even an object the user may not see answers 204.
	omer := login(t, f.srv, "omer", testPassword)
	resp = omer.do(http.MethodDelete, "/api/focus", FocusRequest{Kind: "service", ID: id}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE for an invisible object = %d, want 204", resp.StatusCode)
	}
}

func TestFocus_RequiresCSRF(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	resp := sam.do(http.MethodPost, "/api/focus", FocusRequest{Kind: "service", ID: focusServiceKey("payments-api").ID()}, map[string]string{"X-CSRF-Token": ""})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST without a CSRF token = %d, want 403", resp.StatusCode)
	}
}

func TestFocus_CountsAdmissions(t *testing.T) {
	f := newFocusFixture(t, nil)
	sam := login(t, f.srv, "sam", testPassword)
	id := focusServiceKey("payments-api").ID()
	postFocus(t, sam, "service", id, true)
	postFocus(t, sam, "service", id, false)
	postFocus(t, sam, "service", "not-an-id", false)
	var buf bytes.Buffer
	if err := f.tel.WritePrometheus(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`harbormaster_focus_admissions_total{result="admitted"} 1`,
		`harbormaster_focus_admissions_total{result="refreshed"} 1`,
		`harbormaster_focus_admissions_total{result="not_found"} 1`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("metrics missing %q\n%s", want, buf.String())
		}
	}
}
