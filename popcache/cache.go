package main

import (
	"container/list"
	"fmt"
	"net/http"
	"reflect"
	"sync"
	"time"
)

type cacheEntry struct {
	statusCode int
	status     string
	header     http.Header
	body       []byte
	storedAt   time.Time
	initialAge time.Duration
	// freshnessLifetimeSet distinguishes an explicit max-age=0 from an
	// entry created directly by tests or older callers that needs the TTL
	// fallback supplied to set.
	freshnessLifetime    time.Duration
	freshnessLifetimeSet bool
	vary                 []string
	varyValues           http.Header
	staleWhileRevalidate time.Duration
	staleIfError         time.Duration
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
	entries        map[string][]*list.Element
	fastEntries    sync.Map // key -> *cacheItem for single, non-Vary variants
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
		entries:        make(map[string][]*list.Element),
		lru:            list.New(),
		maxBytes:       maxBytes,
		maxObjectBytes: maxObjectBytes,
	}, nil
}

func (c *memoryCache) get(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elements := c.entries[key]
	if len(elements) == 0 {
		return cacheEntry{}, false
	}
	for _, element := range append([]*list.Element(nil), elements...) {
		item := element.Value.(*cacheItem)
		if !item.entry.isFresh(time.Now()) {
			c.removeElementLocked(element)
			continue
		}
		c.lru.MoveToFront(element)
		return cloneCacheEntryForRead(item.entry), true
	}
	return cacheEntry{}, false
}

// getFresh returns only a fresh entry and leaves stale entries available for
// conditional revalidation.
func (c *memoryCache) getFresh(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, element := range c.entries[key] {
		item := element.Value.(*cacheItem)
		if item.entry.isFresh(time.Now()) {
			c.lru.MoveToFront(element)
			return cloneCacheEntryForRead(item.entry), true
		}
	}
	return cacheEntry{}, false
}

func (c *memoryCache) getFreshVariant(key string, req *http.Request) (cacheEntry, bool) {
	return c.getFreshVariantAt(key, req, time.Now())
}

func (c *memoryCache) getFreshVariantAt(key string, req *http.Request, now time.Time) (cacheEntry, bool) {
	if value, ok := c.fastEntries.Load(key); ok {
		item := value.(*cacheItem)
		if item.entry.isFresh(now) && item.entry.matchesRequest(req) {
			return cloneCacheEntryForRead(item.entry), true
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, element := range c.entries[key] {
		item := element.Value.(*cacheItem)
		if item.entry.isFresh(now) && item.entry.matchesRequest(req) {
			c.lru.MoveToFront(element)
			return cloneCacheEntryForRead(item.entry), true
		}
	}
	return cacheEntry{}, false
}

// peek returns an entry even when it is stale. A stale entry can still carry
// validators such as ETag or Last-Modified that are needed for revalidation.
func (c *memoryCache) peek(key string) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, element := range c.entries[key] {
		return cloneCacheEntryForRead(element.Value.(*cacheItem).entry), true
	}
	return cacheEntry{}, false
}

func (c *memoryCache) peekVariant(key string, req *http.Request) (cacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, element := range c.entries[key] {
		item := element.Value.(*cacheItem)
		if item.entry.matchesRequest(req) {
			return cloneCacheEntryForRead(item.entry), true
		}
	}
	return cacheEntry{}, false
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

	for _, existing := range append([]*list.Element(nil), c.entries[key]...) {
		if existing.Value.(*cacheItem).entry.sameVariant(entry) {
			c.removeElementLocked(existing)
		}
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
	c.entries[key] = append(c.entries[key], element)
	if len(c.entries[key]) == 1 && len(storedEntry.vary) == 0 {
		c.fastEntries.Store(key, item)
	} else {
		c.fastEntries.Delete(key)
	}
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

func (entry cacheEntry) servesStaleWithin(now time.Time, window time.Duration) bool {
	return !entry.isFresh(now) && entry.currentAge(now) < entry.freshnessLifetime+window
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
	entries := 0
	for _, elements := range c.entries {
		entries += len(elements)
	}
	return cacheStats{
		Entries:        entries,
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
	if value, ok := c.fastEntries.Load(item.key); ok && value.(*cacheItem) == item {
		c.fastEntries.Delete(item.key)
	}
	elements := c.entries[item.key]
	for index, candidate := range elements {
		if candidate == element {
			elements = append(elements[:index], elements[index+1:]...)
			break
		}
	}
	if len(elements) == 0 {
		delete(c.entries, item.key)
	} else {
		c.entries[item.key] = elements
		remaining := elements[0].Value.(*cacheItem)
		if len(elements) == 1 && len(remaining.entry.vary) == 0 {
			c.fastEntries.Store(item.key, remaining)
		}
	}
	c.lru.Remove(element)
	c.usedBytes -= item.sizeBytes
}

// cacheEntrySize counts the response bytes retained by the cache. Go map,
// list and allocation overhead are intentionally excluded from this value.
func cacheEntrySize(key string, entry cacheEntry) int64 {
	sizeBytes := int64(len(key) + len(entry.status) + len(entry.body))
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
	entry.vary = append([]string(nil), entry.vary...)
	entry.varyValues = entry.varyValues.Clone()
	return entry
}

func (entry cacheEntry) matchesRequest(req *http.Request) bool {
	for _, name := range entry.vary {
		if !reflect.DeepEqual(entry.varyValues.Values(name), req.Header.Values(name)) {
			return false
		}
	}
	return true
}

func (entry cacheEntry) sameVariant(other cacheEntry) bool {
	return reflect.DeepEqual(entry.vary, other.vary) &&
		reflect.DeepEqual(entry.varyValues, other.varyValues)
}

// Cache reads return an entry with immutable header and body data. The
// transport clones the header when constructing the response because it adds
// per-response status and Age fields.
func cloneCacheEntryForRead(entry cacheEntry) cacheEntry {
	return entry
}
