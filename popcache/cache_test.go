package main

import (
	"net/http"
	"testing"
	"time"
)

func TestMemoryCache(t *testing.T) {
	cache := newMemoryCache()

	entry := cacheEntry{
		statusCode: http.StatusOK,
		header: http.Header{
			"Content-Type": {"text/plain"},
		},
		body: []byte("hello"),
	}

	cache.set("/hello", entry, time.Minute)

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
	cache := newMemoryCache()

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
}
