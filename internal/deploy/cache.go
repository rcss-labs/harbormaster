package deploy

import (
	"sync"
	"time"
)

// maxCacheEntries bounds a ttlCache: keys come from request input (service and task definition names), so an entry
// that is never read again would otherwise stay for the life of the process.
const maxCacheEntries = 1024

// ttlCache is a small in-process cache whose entries expire after ttl and that holds at most maxCacheEntries.
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
	for k, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, k)
		}
	}
	if _, replacing := c.entries[key]; !replacing && len(c.entries) >= maxCacheEntries {
		// Still full of live entries: drop the one closest to expiry (entries share one ttl, so the oldest).
		var oldest string
		var oldestExpires time.Time
		for k, entry := range c.entries {
			if oldest == "" || entry.expires.Before(oldestExpires) {
				oldest, oldestExpires = k, entry.expires
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = ttlEntry[V]{value: value, expires: now.Add(c.ttl)}
}

func (c *ttlCache[V]) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}
