package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rcss-labs/harbormaster/internal/agent/provider"
	"github.com/rcss-labs/harbormaster/internal/agent/provider/fake"
	"github.com/rcss-labs/harbormaster/internal/apitoken"
)

func setChecks(t *testing.T, perAddress, global int, wait time.Duration) {
	t.Helper()
	oldPer, oldGlobal, oldWait := whoamiPerAddress, whoamiGlobal, whoamiWait
	whoamiPerAddress, whoamiGlobal, whoamiWait = perAddress, global, wait
	t.Cleanup(func() { whoamiPerAddress, whoamiGlobal, whoamiWait = oldPer, oldGlobal, oldWait })
}

// blockedJunkHarness answers whoami at once for the harness token and holds every other token until released.
func blockedJunkHarness(t *testing.T, edit func(*Config)) (h *harness, release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	h = newHarness(t, harnessOptions{
		edit: func(c *Config) {
			c.TrustedProxy = true
			if edit != nil {
				edit(c)
			}
		},
		whoami: func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "Bearer "+h.token {
				_, _ = io.WriteString(w, `{"token":{"kind":"run"}}`)
				return
			}
			select {
			case <-gate:
				w.WriteHeader(http.StatusUnauthorized)
			case <-r.Context().Done():
			}
		},
	})
	t.Cleanup(release)
	return h, release
}

// call sends one run request as the given client address (the load balancer's X-Forwarded-For entry).
func (h *harness) call(ctx context.Context, token, forwardedFor string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.server.URL+"/v1/runs", bytes.NewReader(h.runBody(nil)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if forwardedFor != "" {
		req.Header.Set("X-Forwarded-For", forwardedFor)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func (h *harness) whoamiArrivals() int {
	n := 0
	for _, r := range h.upstream.Seen() {
		if r.EscapedPath == "/api/tokens/current" {
			n++
		}
	}
	return n
}

// flood sends n requests with made-up (well-formed) run tokens, each from forwardedFor(i), and returns how many got 429.
func flood(t *testing.T, h *harness, n int, forwardedFor func(i int) string) (busy *atomic.Int32, wait func()) {
	t.Helper()
	busy = new(atomic.Int32)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		junk := newRunToken(t, apitoken.KindRun)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if status, err := h.call(ctx, junk, forwardedFor(i)); err == nil && status == http.StatusTooManyRequests {
				busy.Add(1)
			}
		}()
	}
	return busy, wg.Wait
}

func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAuth_OneAddressCannotExhaustTheChecksForOthers(t *testing.T) {
	setChecks(t, 4, 8, 300*time.Millisecond)
	h, release := blockedJunkHarness(t, nil)

	busy, wait := flood(t, h, 30, func(int) string { return "10.0.0.1" })
	waitUntil(t, "the flooding address to hold its checks", func() bool { return h.whoamiArrivals() >= 4 })

	// A valid run from another address still starts, though the flood is going on.
	status, err := h.call(mustTimeout(t, 5*time.Second), h.token, "10.0.0.2")
	if err != nil || status != http.StatusOK {
		t.Errorf("a run from another address: status %d, err %v", status, err)
	}
	time.Sleep(500 * time.Millisecond) // past the wait: the overflow has been answered
	release()
	wait()
	if busy.Load() == 0 {
		t.Error("the flooding address was never told to slow down")
	}
	if n := h.whoamiArrivals(); n > 4+1 { // its 4 held checks plus the valid run's
		t.Errorf("%d checks reached Harbormaster from one address, want at most 4", n-1)
	}
}

func TestAuth_ChecksAreBoundedOverall(t *testing.T) {
	setChecks(t, 4, 2, 300*time.Millisecond)
	h, _ := blockedJunkHarness(t, nil)
	flood(t, h, 2, func(i int) string { return fmt.Sprintf("10.0.1.%d", i) })
	waitUntil(t, "two held checks", func() bool { return h.whoamiArrivals() >= 2 })
	status, err := h.call(mustTimeout(t, 5*time.Second), h.token, "10.0.9.9")
	if err != nil || status != http.StatusTooManyRequests {
		t.Errorf("status %d, err %v, want 429 with every check slot taken", status, err)
	}
}

func TestAuth_TheCallerAddressIsTheProxysEntryNotAClaim(t *testing.T) {
	setChecks(t, 4, 64, 300*time.Millisecond)
	// Trusted proxy: the rightmost X-Forwarded-For entry is the caller; names to its left are claims.
	h, release := blockedJunkHarness(t, nil)
	_, wait := flood(t, h, 20, func(i int) string { return fmt.Sprintf("1.1.1.%d, 10.0.0.7", i) })
	time.Sleep(600 * time.Millisecond)
	arrivals := h.whoamiArrivals()
	release()
	wait()
	if n := arrivals; n > 4 {
		t.Errorf("trusted proxy: %d checks from one caller behind spoofed entries, want at most 4", n)
	}

	// No proxy: the peer address counts and X-Forwarded-For is ignored altogether.
	h2, release2 := blockedJunkHarness(t, func(c *Config) { c.TrustedProxy = false })
	_, wait = flood(t, h2, 20, func(i int) string { return fmt.Sprintf("2.2.2.%d", i) })
	time.Sleep(600 * time.Millisecond)
	arrivals = h2.whoamiArrivals()
	release2()
	wait()
	if n := arrivals; n > 4 {
		t.Errorf("no proxy: %d checks from one peer behind spoofed entries, want at most 4", n)
	}
}

func TestAuth_EveryPathGivesItsCheckSlotBack(t *testing.T) {
	setChecks(t, 3, 10, 200*time.Millisecond)
	oldTimeout := whoamiTimeout
	whoamiTimeout = 150 * time.Millisecond
	t.Cleanup(func() { whoamiTimeout = oldTimeout })

	var mode atomic.Value
	mode.Store("ok")
	h := newHarness(t, harnessOptions{
		providers: map[string]provider.Provider{"fake": fake.New(fake.Text("a", provider.Usage{InputTokens: 1, OutputTokens: 1}))},
		edit:      func(c *Config) { c.TrustedProxy = true },
		whoami: func(w http.ResponseWriter, r *http.Request) {
			switch mode.Load().(string) {
			case "refused":
				w.WriteHeader(http.StatusUnauthorized)
			case "error":
				w.WriteHeader(http.StatusInternalServerError)
			case "not json":
				_, _ = io.WriteString(w, "<html>")
			case "wrong kind":
				_, _ = io.WriteString(w, `{"token":{"kind":"pat"}}`)
			case "slow":
				<-r.Context().Done()
			default:
				_, _ = io.WriteString(w, `{"token":{"kind":"run"}}`)
			}
		},
	})
	for _, name := range []string{"refused", "error", "not json", "wrong kind", "slow"} {
		mode.Store(name)
		for i := 0; i < 8; i++ { // more than the 3 slots one address has
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			status, err := h.call(ctx, h.token, "10.0.0.5")
			cancel()
			if err != nil || status == http.StatusTooManyRequests {
				t.Fatalf("%s, request %d: status %d, err %v (a slot was leaked)", name, i, status, err)
			}
		}
	}
	// A client that gives up while its check is running.
	mode.Store("slow")
	oldTimeout2 := whoamiTimeout
	whoamiTimeout = 5 * time.Second
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		_, _ = h.call(ctx, h.token, "10.0.0.5")
		cancel()
	}
	whoamiTimeout = oldTimeout2
	mode.Store("ok")
	// Abandoned checks end when the server notices the closed connection; a leak would never end.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status, err := h.call(mustTimeout(t, 5*time.Second), h.token, "10.0.0.5")
		if err == nil && status == http.StatusOK {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("a good request at the end: status %d, err %v (a slot was leaked)", status, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestAuth_AWaitingRequestIsEndedByItsContext(t *testing.T) {
	setChecks(t, 1, 8, 5*time.Second)
	h, _ := blockedJunkHarness(t, nil)
	flood(t, h, 1, func(int) string { return "10.0.0.1" })
	waitUntil(t, "the held check", func() bool { return h.whoamiArrivals() >= 1 })
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := h.call(ctx, newRunToken(t, apitoken.KindRun), "10.0.0.1")
	if err == nil {
		t.Error("the waiting request was answered")
	}
	if time.Since(started) > 2*time.Second {
		t.Error("the waiting request outlived its context")
	}
}

func TestRuns_TheConfiguredRunLimitsReachTheLoop(t *testing.T) {
	// A token budget the tools alone exceed: no model call, a limit notice.
	demo := fake.Demo()
	h := newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": demo}, edit: func(c *Config) { c.MaxRunTokens = 100 }})
	events := readEvents(t, h.post(t, "/v1/runs", h.token, h.runBody(nil)))
	if n := len(demo.Requests()); n != 0 || !hasNotice(events, "limit_reached") {
		t.Errorf("MaxRunTokens 100: %d model calls, events %+v", n, events)
	}

	// A run timeout shorter than the model's answer: the run is stopped, not left hanging.
	blocking := newBlockingProvider()
	h = newHarness(t, harnessOptions{providers: map[string]provider.Provider{"fake": blocking}, edit: func(c *Config) { c.RunTimeout = 300 * time.Millisecond }})
	started := time.Now()
	resp := h.postContext(t, mustTimeout(t, 5*time.Second), "/v1/runs", h.token, h.runBody(nil))
	events = readEvents(t, resp)
	if !hasNotice(events, "limit_reached") || lastEvent(t, events).Type != "done" {
		t.Errorf("events = %+v", events)
	}
	if time.Since(started) > 3*time.Second {
		t.Error("the run outlived its timeout")
	}
}

func TestAuth_AUserWithThousandsOfTagsStillWorks(t *testing.T) {
	var tags strings.Builder
	for i := 0; i < 20000; i++ {
		if i > 0 {
			tags.WriteByte(',')
		}
		fmt.Fprintf(&tags, `{"id":%d,"name":"tag-%d","can_view":true,"can_logs":false}`, i, i)
	}
	body := `{"user":{"id":1,"username":"u"},"token":{"kind":"run","scope":"read"},"tags":[` + tags.String() + `]}`
	h := newHarness(t, harnessOptions{whoami: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) }})
	if len(body) < 1<<20/2 {
		t.Fatalf("the answer is only %d bytes: it must exceed the upstream read cap", len(body))
	}
	if resp := h.post(t, "/v1/runs", h.token, h.runBody(nil)); resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestCheckLimiter_SlotsComeBackAndIdleAddressesAreForgotten(t *testing.T) {
	setChecks(t, 2, 4, 50*time.Millisecond)
	l := newCheckLimiter(whoamiGlobal)
	ctx := context.Background()

	// Two holders at one address, one released: the address has room again while the other still holds.
	first, ok1 := l.acquire(ctx, "a")
	second, ok2 := l.acquire(ctx, "a")
	if !ok1 || !ok2 {
		t.Fatal("the first two checks were refused")
	}
	if _, ok := l.acquire(ctx, "a"); ok {
		t.Fatal("a third check at an address limited to 2 got a slot")
	}
	first()
	third, ok := l.acquire(ctx, "a")
	if !ok {
		t.Fatal("a released address slot was not given back while another check still held one")
	}
	// The global total is back too: three more addresses fit beside the two held ones.
	var held []func()
	for _, addr := range []string{"b", "c"} {
		release, ok := l.acquire(ctx, addr)
		if !ok {
			t.Fatalf("address %s was refused with global room", addr)
		}
		held = append(held, release)
	}
	second()
	third()
	for _, release := range held {
		release()
	}
	for i := 0; i < 50; i++ {
		release, ok := l.acquire(ctx, fmt.Sprintf("addr-%d", i))
		if !ok {
			t.Fatalf("address %d refused: the global slots were not all given back", i)
		}
		release()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.addrs) != 0 {
		t.Errorf("%d idle addresses are still tracked", len(l.addrs))
	}
}
