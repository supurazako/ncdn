package main

import (
	"container/list"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type cacheEntry struct {
	statusCode int
	header     http.Header
	body       []byte
	storedAt   time.Time
	initialAge time.Duration
	// freshnessLifetimeSet distinguishes an explicit max-age=0 from an
	// entry created directly by tests or older callers that needs the TTL
	// fallback supplied to set.
	freshnessLifetime    time.Duration
	freshnessLifetimeSet bool
}

type cacheItem struct {
	key       string
	entry     cacheEntry
	sizeBytes int64
}

type cacheStats struct {
	Entries        int   `json:"entries"`
	UsedBytes      int64 `json:"used_bytes"`
	MaxBytes       int64 `json:"max_bytes"`
	MaxObjectBytes int64 `json:"max_object_bytes"`
}

type memoryCache struct {
	mu             sync.Mutex
	entries        map[string]*list.Element
	lru            *list.List
	usedBytes      int64
	maxBytes       int64
	maxObjectBytes int64
}

func newMemoryCache(maxBytes, maxObjectBytes int64) (*memoryCache, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("cache max bytes must be positive: %d", maxBytes)
	}
	if maxObjectBytes <= 0 {
		return nil, fmt.Errorf(
			"cache max object bytes must be positive: %d",
			maxObjectBytes,
		)
	}
	if maxObjectBytes > maxBytes {
		return nil, fmt.Errorf(
			"cache max object bytes must not exceed cache max bytes: %d > %d",
			maxObjectBytes,
			maxBytes,
		)
	}

	return &memoryCache{
		entries:        make(map[string]*list.Element),
		lru:            list.New(),
		maxBytes:       maxBytes,
		maxObjectBytes: maxObjectBytes,
	}, nil
}

func (c *memoryCache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}

	item := element.Value.(*cacheItem)
	if !item.entry.isFresh(time.Now()) {
		c.removeElementLocked(element)
		return cacheEntry{}, false
	}

	c.lru.MoveToFront(element)
	return cloneCacheEntryForRead(item.entry), true
}

// getFresh returns only a fresh entry and leaves stale entries available for
// conditional revalidation.
func (c *memoryCache) getFresh(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	item := element.Value.(*cacheItem)
	if !item.entry.isFresh(time.Now()) {
		return cacheEntry{}, false
	}
	c.lru.MoveToFront(element)
	return cloneCacheEntryForRead(item.entry), true
}

// peek returns an entry even when it is stale. A stale entry can still carry
// validators such as ETag or Last-Modified that are needed for revalidation.
func (c *memoryCache) peek(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	element, ok := c.entries[key]
	if !ok {
		return cacheEntry{}, false
	}
	return cloneCacheEntryForRead(element.Value.(*cacheItem).entry), true
}

func (c *memoryCache) set(
	key string,
	entry cacheEntry,
	ttl time.Duration,
) bool {
	if !entry.freshnessLifetimeSet {
		entry.freshnessLifetime = ttl
	}
	entry.storedAt = time.Now()
	sizeBytes := cacheEntrySize(key, entry)
	if sizeBytes > c.maxObjectBytes || sizeBytes > c.maxBytes {
		return false
	}

	storedEntry := cloneCacheEntry(entry)

	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.entries[key]; ok {
		c.removeElementLocked(existing)
	}

	for c.usedBytes+sizeBytes > c.maxBytes {
		oldest := c.lru.Back()
		if oldest == nil {
			return false
		}
		c.removeElementLocked(oldest)
	}

	item := &cacheItem{
		key:       key,
		entry:     storedEntry,
		sizeBytes: sizeBytes,
	}
	element := c.lru.PushFront(item)
	c.entries[key] = element
	c.usedBytes += sizeBytes
	return true
}

func (entry cacheEntry) currentAge(now time.Time) time.Duration {
	age := entry.initialAge
	if !entry.storedAt.IsZero() {
		age += now.Sub(entry.storedAt)
	}
	if age < 0 {
		return 0
	}
	return age
}

func (entry cacheEntry) isFresh(now time.Time) bool {
	return entry.currentAge(now) < entry.freshnessLifetime
}

// maxCacheableBodyBytes returns how much response body can be stored after
// accounting for the cache key and response headers.
func (c *memoryCache) maxCacheableBodyBytes(
	key string,
	entry cacheEntry,
) int64 {
	return c.maxObjectBytes - cacheEntrySize(key, entry)
}

func (c *memoryCache) stats() cacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.removeExpiredLocked(time.Now())
	return cacheStats{
		Entries:        len(c.entries),
		UsedBytes:      c.usedBytes,
		MaxBytes:       c.maxBytes,
		MaxObjectBytes: c.maxObjectBytes,
	}
}

func (c *memoryCache) removeExpiredLocked(now time.Time) {
	for element := c.lru.Back(); element != nil; {
		previous := element.Prev()
		item := element.Value.(*cacheItem)
		if !item.entry.isFresh(now) {
			c.removeElementLocked(element)
		}
		element = previous
	}
}

func (c *memoryCache) removeElementLocked(element *list.Element) {
	item := element.Value.(*cacheItem)
	delete(c.entries, item.key)
	c.lru.Remove(element)
	c.usedBytes -= item.sizeBytes
}

// cacheEntrySize counts the response bytes retained by the cache. Go map,
// list and allocation overhead are intentionally excluded from this value.
func cacheEntrySize(key string, entry cacheEntry) int64 {
	sizeBytes := int64(len(key) + len(entry.body))
	for name, values := range entry.header {
		sizeBytes += int64(len(name))
		for _, value := range values {
			sizeBytes += int64(len(value))
		}
	}
	return sizeBytes
}

func cloneCacheEntry(entry cacheEntry) cacheEntry {
	entry.header = entry.header.Clone()
	entry.body = append([]byte(nil), entry.body...)
	return entry
}

// Cache hits can share the immutable stored body. Only the header is cloned
// because the transport adds the cache status header to each response.
func cloneCacheEntryForRead(entry cacheEntry) cacheEntry {
	entry.header = entry.header.Clone()
	return entry
}
