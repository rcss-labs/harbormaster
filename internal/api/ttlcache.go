package api

import (
	"sync"
	"time"
)

// ttlCache keeps one loaded value for a short time, so a read that every request or scrape repeats costs one store call
// per window and task. A failed load is not kept. Loads are serialised, so a burst of requests after the value expires
// makes one call, not one each.
type ttlCache[T any] struct {
	mu     sync.Mutex
	loaded time.Time
	value  T
	valid  bool
}

func (c *ttlCache[T]) get(now time.Time, ttl time.Duration, load func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && now.Sub(c.loaded) < ttl {
		return c.value, nil
	}
	value, err := load()
	if err != nil {
		var zero T
		return zero, err
	}
	c.value, c.loaded, c.valid = value, now, true
	return value, nil
}
