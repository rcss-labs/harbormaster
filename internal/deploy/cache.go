package deploy

import (
	"sync"
	"time"
)

// ttlCache is a small in-process cache whose entries expire after ttl.
// Time comes from the caller so tests control it with Service.Now.
type ttlCache[V any] struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]ttlEntry[V]
}

type ttlEntry[V any] struct {
	value   V
	expires time.Time
}

func newTTLCache[V any](ttl time.Duration) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, entries: map[string]ttlEntry[V]{}}
}

func (c *ttlCache[V]) get(key string, now time.Time) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !now.Before(entry.expires) {
		delete(c.entries, key)
		var zero V
		return zero, false
	}
	return entry.value, true
}

func (c *ttlCache[V]) put(key string, value V, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = ttlEntry[V]{value: value, expires: now.Add(c.ttl)}
}

func (c *ttlCache[V]) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
