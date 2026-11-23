package mcpgw

import (
	"math"
	"sync"
	"time"
)

// Failure limiter defaults (design, "Tunable defaults": constants, not settings).
const (
	failureLimit       = 10  // per client IP and token id
	failureIPCeiling   = 100 // per client IP across all token ids
	failureWindow      = time.Minute
	failureLimiterSize = 50000
)

// failureWindowCount is one key's fixed window.
type failureWindowCount struct {
	start time.Time
	count int
}

// ipFailureLimiter counts upstream 401s in fixed windows, under two keys: one
// per client IP and token public id (max per window), and one per client IP
// across all ids (ceiling per window). The pair key keeps one bad token from
// locking out everyone behind a shared egress; the ceiling stops one IP from
// spraying unlimited ids. The id is the public hmp_/hmo_ identifier, never the
// secret. It is per task and best effort: WAF is the real rate limit, so when
// its memory bound is reached it forgets rather than refuses (fails open).
type ipFailureLimiter struct {
	max     int
	ceiling int
	window  time.Duration
	now     func() time.Time

	mu      sync.Mutex
	windows map[string]failureWindowCount
}

func newFailureLimiter(max, ceiling int, window time.Duration, now func() time.Time) *ipFailureLimiter {
	return &ipFailureLimiter{max: max, ceiling: ceiling, window: window, now: now, windows: map[string]failureWindowCount{}}
}

// An IP never contains a "|", so the two key shapes cannot collide.
func ipKey(ip string) string       { return ip }
func pairKey(ip, id string) string { return ip + "|" + id }

// Blocked reports whether the (ip, token id) pair, or the ip as a whole, has
// reached its limit in the current window.
func (l *ipFailureLimiter) Blocked(ip, id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reached(pairKey(ip, id), l.max) || l.reached(ipKey(ip), l.ceiling)
}

// reached is called with mu held.
func (l *ipFailureLimiter) reached(key string, limit int) bool {
	entry, ok := l.windows[key]
	return ok && l.now().Sub(entry.start) < l.window && entry.count >= limit
}

// Failed counts one upstream 401 for the pair and for the ip.
func (l *ipFailureLimiter) Failed(ip, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.bump(pairKey(ip, id))
	l.bump(ipKey(ip))
}

// bump is called with mu held.
func (l *ipFailureLimiter) bump(key string) {
	now := l.now()
	entry, ok := l.windows[key]
	if !ok || now.Sub(entry.start) >= l.window {
		if !ok && len(l.windows) >= failureLimiterSize {
			l.evict(now)
		}
		entry = failureWindowCount{start: now}
	}
	entry.count++
	l.windows[key] = entry
}

// RetryAfter is the time left in the blocking window, at least one second.
func (l *ipFailureLimiter) RetryAfter(ip, id string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	left := time.Duration(0)
	for key, limit := range map[string]int{pairKey(ip, id): l.max, ipKey(ip): l.ceiling} {
		if !l.reached(key, limit) {
			continue
		}
		left = max(left, l.window-l.now().Sub(l.windows[key].start))
	}
	return max(left, time.Second)
}

// evict drops windows that have ended; if none had, it clears the map.
// Called with mu held.
func (l *ipFailureLimiter) evict(now time.Time) {
	for key, entry := range l.windows {
		if now.Sub(entry.start) >= l.window {
			delete(l.windows, key)
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
