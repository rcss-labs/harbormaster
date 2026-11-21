// Package limits holds the limits that are counted across tasks when a store is configured: concurrent sessions
// per user and token buckets or pacers per key. Each has an in-memory implementation (today's per-task behaviour)
// and a Valkey/Redis one.
package limits

import (
	"context"
	"strconv"
	"sync"
)

// Concurrency caps how many sessions of one kind a user holds.
type Concurrency interface {
	// Acquire takes a slot of limit for the user if fewer than max are held. release is idempotent and never blocks.
	Acquire(ctx context.Context, limit string, userID int64, max int) (release func(), ok bool, err error)
	Count(ctx context.Context, limit string, userID int64) (int, error)
}

// memoryConcurrency is the per-task counter: one count per limit and user.
type memoryConcurrency struct {
	mu     sync.Mutex
	counts map[string]int
}

// NewMemoryConcurrency counts within this process only.
func NewMemoryConcurrency() Concurrency {
	return &memoryConcurrency{counts: map[string]int{}}
}

func memoryKey(limit string, userID int64) string { return limit + ":" + strconv.FormatInt(userID, 10) }

func (m *memoryConcurrency) Acquire(_ context.Context, limit string, userID int64, max int) (func(), bool, error) {
	key := memoryKey(limit, userID)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts[key] >= max {
		return func() {}, false, nil
	}
	m.counts[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.counts[key]--; m.counts[key] <= 0 {
				delete(m.counts, key)
			}
		})
	}, true, nil
}

func (m *memoryConcurrency) Count(_ context.Context, limit string, userID int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counts[memoryKey(limit, userID)], nil
}
