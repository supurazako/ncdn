package main

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestMemoryCache(t *testing.T) {
	cache := mustNewMemoryCache(t, 1024, 512)

	entry := cacheEntry{
		statusCode: http.StatusOK,
		header: http.Header{
			"Content-Type": {"text/plain"},
		},
		body: []byte("hello"),
	}

	if !cache.set("/hello", entry, time.Minute) {
		t.Fatal("expected entry to be cached")
	}

	// Verify that the cache owns its copy of mutable response data.
	entry.header.Set("Content-Type", "application/json")
	entry.body[0] = 'H'

	got, ok := cache.get("/hello")
	if !ok {
		t.Fatal("expected cache hit")
	}

	if got.statusCode != http.StatusOK {
		t.Fatalf("unexpected status: %d", got.statusCode)
	}

	if got.header.Get("Content-Type") != "text/plain" {
		t.Fatalf(
			"unexpected content type: %q",
			got.header.Get("Content-Type"),
		)
	}

	if string(got.body) != "hello" {
		t.Fatalf("unexpected body: %q", got.body)
	}
}

func TestMemoryCacheExpires(t *testing.T) {
	cache := mustNewMemoryCache(t, 1024, 512)

	cache.set(
		"/expired",
		cacheEntry{
			statusCode: http.StatusOK,
			body:       []byte("expired"),
		},
		-time.Second,
	)

	if _, ok := cache.get("/expired"); ok {
		t.Fatal("expected cache miss for expired entry")
	}
	stats := cache.stats()
	if stats.Entries != 0 || stats.UsedBytes != 0 {
		t.Fatalf("expired entry remains in cache: %+v", stats)
	}
}

func TestMemoryCacheEvictsLeastRecentlyUsed(t *testing.T) {
	// Each entry is 6 bytes: a 2-byte key and a 4-byte body.
	cache := mustNewMemoryCache(t, 12, 6)
	entry := func(body string) cacheEntry {
		return cacheEntry{statusCode: http.StatusOK, body: []byte(body)}
	}

	cache.set("/a", entry("aaaa"), time.Minute)
	cache.set("/b", entry("bbbb"), time.Minute)
	if _, ok := cache.get("/a"); !ok {
		t.Fatal("expected /a cache hit")
	}
	cache.set("/c", entry("cccc"), time.Minute)

	if _, ok := cache.get("/b"); ok {
		t.Fatal("expected least recently used /b to be evicted")
	}
	if _, ok := cache.get("/a"); !ok {
		t.Fatal("expected recently used /a to remain")
	}
	if _, ok := cache.get("/c"); !ok {
		t.Fatal("expected /c to be cached")
	}
	if stats := cache.stats(); stats.UsedBytes > stats.MaxBytes {
		t.Fatalf("cache exceeds max bytes: %+v", stats)
	}
}

func TestMemoryCacheRejectsLargeObject(t *testing.T) {
	cache := mustNewMemoryCache(t, 16, 6)

	if cache.set(
		"/large",
		cacheEntry{body: []byte("x")},
		time.Minute,
	) {
		t.Fatal("expected object larger than max object bytes to be rejected")
	}
	stats := cache.stats()
	if stats.Entries != 0 || stats.UsedBytes != 0 {
		t.Fatalf("rejected object changed cache stats: %+v", stats)
	}
}

func TestMemoryCacheReplacementUpdatesUsedBytes(t *testing.T) {
	cache := mustNewMemoryCache(t, 32, 16)

	cache.set("/a", cacheEntry{body: []byte("aaaa")}, time.Minute)
	cache.set("/a", cacheEntry{body: []byte("b")}, time.Minute)

	stats := cache.stats()
	if stats.Entries != 1 {
		t.Fatalf("entries: got %d, want 1", stats.Entries)
	}
	if stats.UsedBytes != 3 {
		t.Fatalf("used bytes: got %d, want 3", stats.UsedBytes)
	}
}

func TestNewMemoryCacheValidatesLimits(t *testing.T) {
	tests := []struct {
		name           string
		maxBytes       int64
		maxObjectBytes int64
	}{
		{name: "zero cache size", maxBytes: 0, maxObjectBytes: 1},
		{name: "zero object size", maxBytes: 1, maxObjectBytes: 0},
		{name: "object exceeds cache", maxBytes: 1, maxObjectBytes: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := newMemoryCache(tt.maxBytes, tt.maxObjectBytes); err == nil {
				t.Fatal("expected invalid limits to return an error")
			}
		})
	}
}

func TestMemoryCacheConcurrentAccess(t *testing.T) {
	cache := mustNewMemoryCache(t, 4096, 512)

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				key := fmt.Sprintf("/%d/%d", worker, i%10)
				cache.set(key, cacheEntry{body: []byte(key)}, time.Minute)
				cache.get(key)
				cache.stats()
			}
		}(worker)
	}
	wg.Wait()

	stats := cache.stats()
	if stats.UsedBytes > stats.MaxBytes {
		t.Fatalf("cache exceeds max bytes: %+v", stats)
	}
}

func mustNewMemoryCache(
	t *testing.T,
	maxBytes int64,
	maxObjectBytes int64,
) *memoryCache {
	t.Helper()

	cache, err := newMemoryCache(maxBytes, maxObjectBytes)
	if err != nil {
		t.Fatal(err)
	}
	return cache
}
