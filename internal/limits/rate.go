package limits

import (
	"context"
	"sync"
	"time"
)

// Rate is a family of token buckets and one-per-interval pacers, each addressed by a name and a key.
type Rate interface {
	// Allow takes one token from the bucket name/key and reports whether one was available.
	Allow(ctx context.Context, name, key string, capacity int, refillPerSecond float64) (bool, error)
	// Reserve books the next slot of a one-per-interval pacer and returns how long to wait for it.
	Reserve(ctx context.Context, name, key string, every time.Duration) (wait time.Duration, err error)
}

// maxTracked bounds memory: past it, buckets that have refilled completely and slots in the past are dropped.
const maxTracked = 10000

type bucketState struct {
	tokens, capacity, refill float64
	last                     time.Time
}

type memoryRate struct {
	now func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucketState
	slots   map[string]time.Time
}

// NewMemoryRate keeps the buckets and pacer slots in this process, which is the backend used when no HM_KV_URL is set or the store is down.
func NewMemoryRate(now func() time.Time) Rate {
	if now == nil {
		now = time.Now
	}
	return &memoryRate{now: now, buckets: map[string]*bucketState{}, slots: map[string]time.Time{}}
}

func (m *memoryRate) Allow(_ context.Context, name, key string, capacity int, refillPerSecond float64) (bool, error) {
	id := name + "|" + key
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.buckets[id]
	if !ok {
		if len(m.buckets) >= maxTracked {
			m.pruneBucketsLocked(now)
		}
		state = &bucketState{tokens: float64(capacity), capacity: float64(capacity), refill: refillPerSecond, last: now}
		m.buckets[id] = state
	}
	if elapsed := now.Sub(state.last).Seconds(); elapsed > 0 {
		state.tokens = min(state.capacity, state.tokens+elapsed*state.refill)
		state.last = now
	}
	if state.tokens < 1 {
		return false, nil
	}
	state.tokens--
	return true, nil
}

func (m *memoryRate) pruneBucketsLocked(now time.Time) {
	for id, state := range m.buckets {
		if state.tokens+now.Sub(state.last).Seconds()*state.refill >= state.capacity {
			delete(m.buckets, id)
		}
	}
}

func (m *memoryRate) Reserve(_ context.Context, name, key string, every time.Duration) (time.Duration, error) {
	id := name + "|" + key
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.slots) >= maxTracked {
		for k, next := range m.slots {
			if !next.After(now) {
				delete(m.slots, k)
			}
		}
	}
	slot := now
	if next := m.slots[id]; next.After(now) {
		slot = next
	}
	m.slots[id] = slot.Add(every)
	return slot.Sub(now), nil
}
