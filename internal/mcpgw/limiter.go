package mcpgw

import (
	"math"
	"sync"
	"time"
)

// Failure limiter defaults (design, "Tunable defaults": constants, not settings).
const (
	failureLimit       = 10
	failureWindow      = time.Minute
	failureLimiterSize = 50000
)

// failureWindowCount is one IP's fixed window.
type failureWindowCount struct {
	start time.Time
	count int
}

// ipFailureLimiter counts upstream 401s per client IP in fixed windows. It is
// per task and best effort: WAF is the real rate limit, so when its memory
// bound is reached it forgets rather than refuses (fails open).
type ipFailureLimiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	windows map[string]failureWindowCount
}

func newFailureLimiter(max int, window time.Duration, now func() time.Time) *ipFailureLimiter {
	return &ipFailureLimiter{max: max, window: window, now: now, windows: map[string]failureWindowCount{}}
}

// Blocked reports whether ip has reached the limit in its current window.
func (l *ipFailureLimiter) Blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.windows[ip]
	return ok && l.now().Sub(entry.start) < l.window && entry.count >= l.max
}

// Failed counts one upstream 401 for ip.
func (l *ipFailureLimiter) Failed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	entry, ok := l.windows[ip]
	if !ok || now.Sub(entry.start) >= l.window {
		if !ok && len(l.windows) >= failureLimiterSize {
			l.evict(now)
		}
		entry = failureWindowCount{start: now}
	}
	entry.count++
	l.windows[ip] = entry
}

// RetryAfter is the time left in ip's current window, at least one second.
func (l *ipFailureLimiter) RetryAfter(ip string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry, ok := l.windows[ip]
	if !ok {
		return time.Second
	}
	left := l.window - l.now().Sub(entry.start)
	return max(left, time.Second)
}

// evict drops windows that have ended; if none had, it clears the map.
// Called with mu held.
func (l *ipFailureLimiter) evict(now time.Time) {
	for ip, entry := range l.windows {
		if now.Sub(entry.start) >= l.window {
			delete(l.windows, ip)
		}
	}
	if len(l.windows) >= failureLimiterSize {
		clear(l.windows)
	}
}

// retryAfterSeconds rounds a duration up to whole seconds for Retry-After.
func retryAfterSeconds(d time.Duration) int {
	return int(math.Ceil(d.Seconds()))
}
