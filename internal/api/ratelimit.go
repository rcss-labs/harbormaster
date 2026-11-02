package api

import (
	"sync"
	"time"
)

// ipBucket is an in-process per-IP token bucket. State is per task, which is the intended
// granularity for the SAML login start (spec: 60/min per IP per task).
type ipBucket struct {
	capacity float64
	refill   float64 // tokens per second
	now      func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucketState
}

type bucketState struct {
	tokens float64
	last   time.Time
}

// maxTrackedIPs bounds memory: past it, buckets that have refilled completely are dropped.
const maxTrackedIPs = 10000

func newIPBucket(capacity int, refillPerSecond float64, now func() time.Time) *ipBucket {
	return &ipBucket{capacity: float64(capacity), refill: refillPerSecond, now: now, buckets: map[string]*bucketState{}}
}

// Allow takes one token for ip and reports whether one was available.
func (b *ipBucket) Allow(ip string) bool {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	state, ok := b.buckets[ip]
	if !ok {
		if len(b.buckets) >= maxTrackedIPs {
			b.pruneLocked(now)
		}
		state = &bucketState{tokens: b.capacity, last: now}
		b.buckets[ip] = state
	}
	if elapsed := now.Sub(state.last).Seconds(); elapsed > 0 {
		state.tokens = min(b.capacity, state.tokens+elapsed*b.refill)
		state.last = now
	}
	if state.tokens < 1 {
		return false
	}
	state.tokens--
	return true
}

func (b *ipBucket) pruneLocked(now time.Time) {
	for ip, state := range b.buckets {
		if state.tokens+now.Sub(state.last).Seconds()*b.refill >= b.capacity {
			delete(b.buckets, ip)
		}
	}
}
