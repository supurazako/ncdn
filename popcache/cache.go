package main

import (
	"net/http"
	"sync"
	"time"
)

type cacheEntry struct {
	statusCode int
	header     http.Header
	body       []byte
	expiresAt  time.Time
}

type memoryCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
}

func newMemoryCache() *memoryCache {
	return &memoryCache{
		entries: make(map[string]cacheEntry),
	}
}

func (c *memoryCache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}

	if time.Now().After(entry.expiresAt) {
		delete(c.entries, key)
		return cacheEntry{}, false
	}

	return cloneCacheEntry(entry), true
}

func (c *memoryCache) set(
	key string,
	entry cacheEntry,
	ttl time.Duration,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry.expiresAt = time.Now().Add(ttl)
	c.entries[key] = cloneCacheEntry(entry)
}

func cloneCacheEntry(entry cacheEntry) cacheEntry {
	entry.header = entry.header.Clone()
	entry.body = append([]byte(nil), entry.body...)
	return entry
}
