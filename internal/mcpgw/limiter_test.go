package mcpgw

import (
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)}
}

func TestFailureLimiter_BlocksAfterMaxAndResetsWithTheWindow(t *testing.T) {
	clock := newFakeClock()
	l := newFailureLimiter(10, 1000, time.Minute, clock.Now)
	for i := 0; i < 9; i++ {
		l.Failed("1.1.1.1", "tok")
	}
	if l.Blocked("1.1.1.1", "tok") {
		t.Fatal("blocked after 9 failures")
	}
	l.Failed("1.1.1.1", "tok")
	if !l.Blocked("1.1.1.1", "tok") {
		t.Fatal("not blocked after 10 failures")
	}
	clock.Advance(59 * time.Second)
	if !l.Blocked("1.1.1.1", "tok") {
		t.Error("unblocked before the window ended")
	}
	if got := l.RetryAfter("1.1.1.1", "tok"); got != time.Second {
		t.Errorf("RetryAfter near the end = %s, want 1s", got)
	}
	clock.Advance(2 * time.Second)
	if l.Blocked("1.1.1.1", "tok") {
		t.Error("still blocked after the window")
	}
	l.Failed("1.1.1.1", "tok")
	if l.Blocked("1.1.1.1", "tok") {
		t.Error("the count did not restart in a new window")
	}
}

func TestFailureLimiter_RetryAfterIsTheTimeLeft(t *testing.T) {
	clock := newFakeClock()
	l := newFailureLimiter(1, 1000, time.Minute, clock.Now)
	l.Failed("1.1.1.1", "tok")
	clock.Advance(20 * time.Second)
	if got := l.RetryAfter("1.1.1.1", "tok"); got != 40*time.Second {
		t.Errorf("RetryAfter = %s, want 40s", got)
	}
	if got := l.RetryAfter("9.9.9.9", "tok"); got < time.Second {
		t.Errorf("RetryAfter for an unknown ip = %s, want at least 1s", got)
	}
}

func TestFailureLimiter_IPsAreIndependent(t *testing.T) {
	l := newFailureLimiter(3, 1000, time.Minute, newFakeClock().Now)
	for i := 0; i < 3; i++ {
		l.Failed("1.1.1.1", "tok")
	}
	if !l.Blocked("1.1.1.1", "tok") || l.Blocked("2.2.2.2", "tok") {
		t.Error("one IP's failures leaked to another")
	}
}

func TestFailureLimiter_BoundedMemory(t *testing.T) {
	clock := newFakeClock()
	l := newFailureLimiter(10, 1000, time.Minute, clock.Now)
	for i := 0; i < failureLimiterSize+5000; i++ {
		l.Failed(fmt.Sprintf("ip-%d", i), "tok")
	}
	l.mu.Lock()
	size := len(l.windows)
	l.mu.Unlock()
	if size > failureLimiterSize {
		t.Errorf("%d tracked IPs, bound is %d", size, failureLimiterSize)
	}
	if size == 0 {
		t.Error("map empty after inserts")
	}

	// Expired windows are dropped first, so a live entry (its pair and ip windows) survives a full map.
	l2 := newFailureLimiter(1, 1000, time.Minute, clock.Now)
	for i := 0; i < failureLimiterSize; i++ {
		l2.Failed(fmt.Sprintf("old-%d", i), "tok")
	}
	clock.Advance(2 * time.Minute)
	l2.Failed("fresh", "tok")
	l2.mu.Lock()
	size = len(l2.windows)
	l2.mu.Unlock()
	if size != 2 || !l2.Blocked("fresh", "tok") {
		t.Errorf("after expiry %d entries, fresh blocked=%v; want the expired ones evicted and the new one kept", size, l2.Blocked("fresh", "tok"))
	}
}

func TestFailureLimiter_FailsOpenWhenFullOfLiveEntries(t *testing.T) {
	clock := newFakeClock()
	l := newFailureLimiter(1, 1000, time.Minute, clock.Now)
	for i := 0; i < failureLimiterSize; i++ {
		l.Failed(fmt.Sprintf("ip-%d", i), "tok")
	}
	l.Failed("one-more", "tok") // full of live windows: cleared (fail open), never refused
	if l.Blocked("ip-0", "tok") {
		t.Error("an old entry is still blocked after the clear")
	}
}

func TestFailureLimiter_ConcurrentUse(t *testing.T) {
	l := newFailureLimiter(10, 1000, time.Minute, time.Now)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				l.Failed("1.1.1.1", "tok")
				l.Blocked("1.1.1.1", "tok")
				l.RetryAfter("1.1.1.1", "tok")
			}
		}()
	}
	wg.Wait()
}

func TestHandler_BlocksAnIPAfterTenUpstream401s(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(http.StatusUnauthorized, `{"error":{"code":"unauthenticated","message":"x"}}`)})
	for i := 1; i <= 10; i++ {
		resp := h.callTool(t, "get_overview", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("request %d: status %d, want 401", i, resp.StatusCode)
		}
		readBody(t, resp)
	}
	resp := h.callTool(t, "get_overview", nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("eleventh request: status %d, want 429", resp.StatusCode)
	}
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retry < 1 || retry > 60 {
		t.Errorf("Retry-After = %q, want 1..60 seconds", resp.Header.Get("Retry-After"))
	}
	readBody(t, resp)
	if n := len(h.upstream.seen()); n != 10 {
		t.Errorf("upstream saw %d requests, want 10 (the blocked one must not reach it)", n)
	}
	connect := h.rpc(t, h.authed(), "initialize", map[string]any{})
	if connect.StatusCode != http.StatusTooManyRequests {
		t.Errorf("initialize while blocked: %d, want 429", connect.StatusCode)
	}
	readBody(t, connect)
	if n := len(h.upstream.seen()); n != 10 {
		t.Errorf("upstream saw %d requests after the blocked initialize", n)
	}
}

func TestHandler_TokenCheck401sAlsoCount(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(http.StatusUnauthorized, `{}`)})
	for i := 0; i < 10; i++ {
		readBody(t, h.rpc(t, h.authed(), "tools/list", nil))
	}
	resp := h.rpc(t, h.authed(), "tools/list", nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %d, want 429 after ten failed token checks", resp.StatusCode)
	}
	readBody(t, resp)
}

func TestHandler_BlockIsPerClientIP(t *testing.T) {
	h := newHarness(t, harnessOptions{
		mutateConfig: func(c *Config) { c.TrustedProxy = true },
		upstream:     respondJSON(http.StatusUnauthorized, `{}`),
	})
	call := func(ip string) int {
		headers := h.authed()
		headers["X-Forwarded-For"] = ip
		resp := h.rpc(t, headers, "tools/call", map[string]any{"name": "get_overview"})
		readBody(t, resp)
		return resp.StatusCode
	}
	for i := 0; i < 10; i++ {
		call("198.51.100.1")
	}
	if got := call("198.51.100.1"); got != http.StatusTooManyRequests {
		t.Errorf("the failing IP got %d, want 429", got)
	}
	if got := call("198.51.100.2"); got != http.StatusUnauthorized {
		t.Errorf("another IP got %d, want a normal 401", got)
	}
}

func TestHandler_MalformedTokensNeverCount(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for i := 0; i < 30; i++ {
		readBody(t, h.rpc(t, map[string]string{"Authorization": "Bearer garbage"}, "tools/list", nil))
	}
	resp := h.rpc(t, h.authed(), "tools/list", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200: malformed tokens must not block the IP", resp.StatusCode)
	}
	readBody(t, resp)
}

func TestHandler_ValidCallsNeverCount(t *testing.T) {
	h := newHarness(t, harnessOptions{})
	for i := 0; i < 40; i++ {
		resp := h.callTool(t, "get_overview", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d", i+1, resp.StatusCode)
		}
		readBody(t, resp)
	}
}

func TestHandler_ToolErrorsAreNotAuthFailures(t *testing.T) {
	h := newHarness(t, harnessOptions{upstream: respondJSON(http.StatusForbidden, `{"error":{"code":"forbidden","message":"x"}}`)})
	for i := 0; i < 25; i++ {
		resp := h.callTool(t, "get_overview", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("request %d: status %d, a 403 is a tool error and must not count", i+1, resp.StatusCode)
		}
		readBody(t, resp)
	}
}

func TestFailureLimiter_OneTokensFailuresDoNotBlockAnotherTokenOnTheSameIP(t *testing.T) {
	l := newFailureLimiter(10, 100, time.Minute, newFakeClock().Now)
	for i := 0; i < 10; i++ {
		l.Failed("203.0.113.7", "hmp_aaaa")
	}
	if !l.Blocked("203.0.113.7", "hmp_aaaa") {
		t.Fatal("the failing token is not blocked after 10 failures")
	}
	if l.Blocked("203.0.113.7", "hmp_bbbb") {
		t.Error("another token behind the same IP was blocked by the first token's failures")
	}
}

func TestFailureLimiter_PerIPCeilingStopsSprayingManyIDs(t *testing.T) {
	l := newFailureLimiter(10, 100, time.Minute, newFakeClock().Now)
	for i := 0; i < 99; i++ {
		l.Failed("203.0.113.7", fmt.Sprintf("hmp_%d", i))
	}
	if l.Blocked("203.0.113.7", "hmp_fresh") {
		t.Fatal("blocked below the per-IP ceiling")
	}
	l.Failed("203.0.113.7", "hmp_99")
	if !l.Blocked("203.0.113.7", "hmp_fresh") {
		t.Error("an IP past the ceiling across many token ids is not blocked")
	}
	if l.Blocked("198.51.100.1", "hmp_fresh") {
		t.Error("the ceiling leaked to another IP")
	}
}

func TestFailureLimiterDefaults(t *testing.T) {
	if failureIPCeiling < 10*failureLimit {
		t.Errorf("failureIPCeiling = %d, want much higher than the per-token limit %d", failureIPCeiling, failureLimit)
	}
}
