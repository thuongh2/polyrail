package gateway

import (
	"sync"
	"time"
)

// ttlCache is a bounded map with expiry. When full it evicts arbitrary entries (Go map order).
// shortcut: random eviction instead of LRU; fine for auth and response caches, upgrade if hit rate suffers.
type ttlCache[V any] struct {
	mu         sync.Mutex
	m          map[string]ttlEntry[V]
	maxEntries int
	maxBytes   int
	bytes      int
}

type ttlEntry[V any] struct {
	v    V
	size int
	exp  time.Time
}

func newTTLCache[V any](maxEntries, maxBytes int) *ttlCache[V] {
	return &ttlCache[V]{m: make(map[string]ttlEntry[V]), maxEntries: maxEntries, maxBytes: maxBytes}
}

func (c *ttlCache[V]) Get(k string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[k]
	if !ok || time.Now().After(e.exp) {
		var zero V
		return zero, false
	}
	return e.v, true
}

// Set stores v with the given size (bytes, for the byte bound) and ttl.
func (c *ttlCache[V]) Set(k string, v V, size int, ttl time.Duration) {
	if c.maxBytes > 0 && size > c.maxBytes/4 {
		return // never let one entry dominate the cache
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.del(k)
	for k2 := range c.m {
		if len(c.m) < c.maxEntries && (c.maxBytes == 0 || c.bytes+size <= c.maxBytes) {
			break
		}
		c.del(k2)
	}
	c.m[k] = ttlEntry[V]{v: v, size: size, exp: time.Now().Add(ttl)}
	c.bytes += size
}

func (c *ttlCache[V]) Delete(k string) {
	c.mu.Lock()
	c.del(k)
	c.mu.Unlock()
}

func (c *ttlCache[V]) del(k string) {
	if e, ok := c.m[k]; ok {
		c.bytes -= e.size
		delete(c.m, k)
	}
}

// Clear drops every entry.
func (c *ttlCache[V]) Clear() {
	c.mu.Lock()
	c.m, c.bytes = make(map[string]ttlEntry[V]), 0
	c.mu.Unlock()
}

func (c *ttlCache[V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}
