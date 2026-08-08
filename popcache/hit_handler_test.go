package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCacheHitHandlerServesFreshCacheWithoutCallingNext(t *testing.T) {
	cache := mustNewMemoryCache(t, 1024, 512)
	request := httptest.NewRequest(http.MethodGet, "http://origin.example/object", nil)
	entry := cacheEntry{
		statusCode:           http.StatusOK,
		status:               "200 OK",
		header:               http.Header{"Content-Type": {"text/plain"}},
		body:                 []byte("cached"),
		freshnessLifetime:    time.Minute,
		freshnessLifetimeSet: true,
		fastKey:              newRequestCacheKey(request),
	}
	if !cache.set(request.URL.String(), entry, time.Minute) {
		t.Fatal("failed to seed cache")
	}
	nextCalled := false
	handler := &cacheHitHandler{
		cache: cache,
		next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			nextCalled = true
		}),
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if nextCalled {
		t.Fatal("next handler was called for a cache hit")
	}
	if response.Code != http.StatusOK || response.Body.String() != "cached" {
		t.Fatalf("response: status=%d body=%q", response.Code, response.Body.String())
	}
	if response.Header().Get(cacheStatusHeader) != "HIT" {
		t.Fatalf("cache status: got %q", response.Header().Get(cacheStatusHeader))
	}
}

func TestCacheHitHandlerDelegatesMiss(t *testing.T) {
	cache := mustNewMemoryCache(t, 1024, 512)
	handler := &cacheHitHandler{
		cache: cache,
		next: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.Copy(w, strings.NewReader("origin"))
		}),
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "http://origin.example/miss", nil))
	if response.Code != http.StatusOK || response.Body.String() != "origin" {
		t.Fatalf("delegated response: status=%d body=%q", response.Code, response.Body.String())
	}
}
