package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCachingTransportReturnsCachedResponse(t *testing.T) {
	originRequests := 0
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests++

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header: http.Header{
				"Content-Type": {"text/plain"},
			},
			Body:    io.NopCloser(strings.NewReader("from origin")),
			Request: req,
		}, nil
	})

	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)

	first := performRequest(t, transport, http.MethodGet)
	if first.cacheStatus != "MISS" {
		t.Fatalf("first request cache status: %q", first.cacheStatus)
	}

	second := performRequest(t, transport, http.MethodGet)
	if second.cacheStatus != "HIT" {
		t.Fatalf("second request cache status: %q", second.cacheStatus)
	}

	if second.body != "from origin" {
		t.Fatalf("unexpected cached body: %q", second.body)
	}

	if originRequests != 1 {
		t.Fatalf("origin requests: got %d, want 1", originRequests)
	}
}

func TestFreshnessLifetimeUsesSharedCachePrecedence(t *testing.T) {
	receivedAt := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{
			name:   "s-maxage overrides max-age",
			header: http.Header{"Cache-Control": {"max-age=60, s-maxage=600"}},
			want:   10 * time.Minute,
		},
		{
			name:   "max-age",
			header: http.Header{"Cache-Control": {"max-age=60"}},
			want:   time.Minute,
		},
		{
			name: "Expires minus Date",
			header: http.Header{
				"Date":    {receivedAt.Format(http.TimeFormat)},
				"Expires": {receivedAt.Add(2 * time.Minute).Format(http.TimeFormat)},
			},
			want: 2 * time.Minute,
		},
		{
			name:   "fallback",
			header: make(http.Header),
			want:   30 * time.Second,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resp := &http.Response{Header: test.header}
			if got := freshnessLifetime(resp, 30*time.Second, receivedAt); got != test.want {
				t.Fatalf("freshness lifetime: got %s, want %s", got, test.want)
			}
		})
	}
}

func TestResponseFromCacheSetsAge(t *testing.T) {
	now := time.Now()
	entry := cacheEntry{
		statusCode:           http.StatusOK,
		header:               make(http.Header),
		body:                 []byte("cached"),
		storedAt:             now.Add(-42 * time.Second),
		initialAge:           3 * time.Second,
		freshnessLifetime:    time.Minute,
		freshnessLifetimeSet: true,
	}
	req, err := http.NewRequest(http.MethodGet, "http://origin.example/object", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp := responseFromCache(req, entry)
	if got := resp.Header.Get("Age"); got != "45" && got != "46" {
		t.Fatalf("Age: got %q, want approximately 45 seconds", got)
	}
}

func TestCachingTransportExpiresAccordingToMaxAge(t *testing.T) {
	var originRequests int
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests++
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Cache-Control": {"max-age=0"}},
			Body:       io.NopCloser(strings.NewReader("from origin")),
			Request:    req,
		}, nil
	})
	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)

	performRequest(t, transport, http.MethodGet)
	performRequest(t, transport, http.MethodGet)

	if originRequests != 2 {
		t.Fatalf("origin requests: got %d, want 2", originRequests)
	}
}

func TestCachingTransportRevalidatesWithETag(t *testing.T) {
	var originRequests int
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests++
		if originRequests == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Cache-Control": {"max-age=0"},
					"Etag":          {`"v1"`},
				},
				Body:    io.NopCloser(strings.NewReader("cached body")),
				Request: req,
			}, nil
		}
		if got := req.Header.Get("If-None-Match"); got != `"v1"` {
			t.Fatalf("If-None-Match: got %q, want %q", got, `"v1"`)
		}
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Header:     http.Header{"Cache-Control": {"max-age=60"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)

	first := performRequest(t, transport, http.MethodGet)
	second := performRequest(t, transport, http.MethodGet)

	if first.body != "cached body" || second.body != "cached body" {
		t.Fatalf("bodies: first=%q second=%q", first.body, second.body)
	}
	if second.cacheStatus != "REVALIDATED" {
		t.Fatalf("cache status: got %q, want REVALIDATED", second.cacheStatus)
	}
	if originRequests != 2 {
		t.Fatalf("origin requests: got %d, want 2", originRequests)
	}
}

func TestCachingTransportRevalidatesWithLastModified(t *testing.T) {
	var originRequests int
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests++
		if originRequests == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header: http.Header{
					"Cache-Control": {"max-age=0"},
					"Last-Modified": {"Thu, 01 Jan 2026 00:00:00 GMT"},
				},
				Body:    io.NopCloser(strings.NewReader("cached body")),
				Request: req,
			}, nil
		}
		if got := req.Header.Get("If-Modified-Since"); got != "Thu, 01 Jan 2026 00:00:00 GMT" {
			t.Fatalf("If-Modified-Since: got %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Header:     http.Header{"Cache-Control": {"max-age=60"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})
	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)

	performRequest(t, transport, http.MethodGet)
	second := performRequest(t, transport, http.MethodGet)
	if second.body != "cached body" || second.cacheStatus != "REVALIDATED" {
		t.Fatalf("revalidated response: %+v", second)
	}
}

func TestCachingTransportDoesNotCachePost(t *testing.T) {
	originRequests := 0
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests++

		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("from origin")),
			Request:    req,
		}, nil
	})

	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)

	performRequest(t, transport, http.MethodPost)
	performRequest(t, transport, http.MethodPost)

	if originRequests != 2 {
		t.Fatalf("origin requests: got %d, want 2", originRequests)
	}
}

func TestCachingTransportDoesNotCachePrivateResponse(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
	}{
		{
			name: "Cache-Control no-store",
			header: http.Header{
				"Cache-Control": {"public, no-store"},
			},
		},
		{
			name: "Cache-Control private is case-insensitive",
			header: http.Header{
				"Cache-Control": {"max-age=60, PRIVATE"},
			},
		},
		{
			name: "Set-Cookie",
			header: http.Header{
				"Set-Cookie": {"session_id=secret"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			originRequests := 0
			origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				originRequests++

				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     test.header.Clone(),
					Body:       io.NopCloser(strings.NewReader("from origin")),
					Request:    req,
				}, nil
			})
			cache := mustNewMemoryCache(t, 1024, 512)
			transport := newCachingTransport(origin, cache, time.Minute)

			first := performRequest(t, transport, http.MethodGet)
			second := performRequest(t, transport, http.MethodGet)

			if first.cacheStatus != "MISS" || second.cacheStatus != "MISS" {
				t.Fatalf(
					"cache status: first=%q second=%q, want both MISS",
					first.cacheStatus,
					second.cacheStatus,
				)
			}
			if originRequests != 2 {
				t.Fatalf("origin requests: got %d, want 2", originRequests)
			}
			if stats := cache.stats(); stats.Entries != 0 {
				t.Fatalf("private response was cached: %+v", stats)
			}
		})
	}
}

func TestCachingTransportStreamsLargeResponseWithoutCaching(t *testing.T) {
	originRequests := 0
	body := strings.Repeat("x", 256)
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests++

		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Header:        make(http.Header),
			Body:          io.NopCloser(strings.NewReader(body)),
			ContentLength: -1,
			Request:       req,
		}, nil
	})
	cache := mustNewMemoryCache(t, 128, 64)
	transport := newCachingTransport(origin, cache, time.Minute)

	first := performRequest(t, transport, http.MethodGet)
	second := performRequest(t, transport, http.MethodGet)

	if first.body != body || second.body != body {
		t.Fatal("large response was not streamed completely")
	}
	if first.cacheStatus != "MISS" || second.cacheStatus != "MISS" {
		t.Fatalf(
			"large response cache status: first=%q second=%q",
			first.cacheStatus,
			second.cacheStatus,
		)
	}
	if originRequests != 2 {
		t.Fatalf("origin requests: got %d, want 2", originRequests)
	}
	if stats := cache.stats(); stats.Entries != 0 || stats.UsedBytes != 0 {
		t.Fatalf("large response changed cache stats: %+v", stats)
	}
}

func TestCachingTransportCoalescesConcurrentMisses(t *testing.T) {
	const requestCount = 8

	originStarted := make(chan struct{})
	releaseOrigin := make(chan struct{})
	var startedOnce sync.Once
	var originRequests atomic.Int64
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests.Add(1)
		startedOnce.Do(func() { close(originStarted) })
		<-releaseOrigin

		return originResponse(req, "from origin"), nil
	})
	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)
	results := make(chan requestResult, requestCount)

	go requestInBackground(transport, context.Background(), results)
	waitForSignal(t, originStarted, "Origin request did not start")
	for range requestCount - 1 {
		go requestInBackground(transport, context.Background(), results)
	}
	waitForMissWaiters(t, transport, requestCount-1)
	close(releaseOrigin)

	hits := 0
	misses := 0
	for range requestCount {
		result := waitForRequestResult(t, results)
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.response.body != "from origin" {
			t.Fatalf("unexpected body: %q", result.response.body)
		}
		switch result.response.cacheStatus {
		case "HIT":
			hits++
		case "MISS":
			misses++
		default:
			t.Fatalf("unexpected cache status: %q", result.response.cacheStatus)
		}
	}

	if got := originRequests.Load(); got != 1 {
		t.Fatalf("origin requests: got %d, want 1", got)
	}
	if hits != requestCount-1 || misses != 1 {
		t.Fatalf("cache statuses: got %d HIT and %d MISS", hits, misses)
	}
}

func TestCachingTransportSharesOriginErrorAndAllowsRetry(t *testing.T) {
	const requestCount = 4

	originStarted := make(chan struct{})
	releaseOrigin := make(chan struct{})
	var startedOnce sync.Once
	var originRequests atomic.Int64
	var failOrigin atomic.Bool
	failOrigin.Store(true)
	originErr := errors.New("Origin is unavailable")
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		originRequests.Add(1)
		startedOnce.Do(func() { close(originStarted) })
		<-releaseOrigin
		if failOrigin.Load() {
			return nil, originErr
		}

		return originResponse(req, "recovered"), nil
	})
	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)
	results := make(chan requestResult, requestCount)

	go requestInBackground(transport, context.Background(), results)
	waitForSignal(t, originStarted, "Origin request did not start")
	for range requestCount - 1 {
		go requestInBackground(transport, context.Background(), results)
	}
	waitForMissWaiters(t, transport, requestCount-1)
	close(releaseOrigin)

	for range requestCount {
		result := waitForRequestResult(t, results)
		if !errors.Is(result.err, originErr) {
			t.Fatalf("request error: got %v, want %v", result.err, originErr)
		}
	}
	if got := originRequests.Load(); got != 1 {
		t.Fatalf("origin requests after failure: got %d, want 1", got)
	}

	failOrigin.Store(false)
	retry := performRequest(t, transport, http.MethodGet)
	if retry.body != "recovered" || retry.cacheStatus != "MISS" {
		t.Fatalf("unexpected retry response: %+v", retry)
	}
	if got := originRequests.Load(); got != 2 {
		t.Fatalf("origin requests after retry: got %d, want 2", got)
	}
}

func TestCachingTransportAllowsWaiterCancellation(t *testing.T) {
	originStarted := make(chan struct{})
	releaseOrigin := make(chan struct{})
	origin := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(originStarted)
		<-releaseOrigin
		return originResponse(req, "from origin"), nil
	})
	transport := newCachingTransport(
		origin,
		mustNewMemoryCache(t, 1024, 512),
		time.Minute,
	)
	leaderResult := make(chan requestResult, 1)
	go requestInBackground(transport, context.Background(), leaderResult)
	waitForSignal(t, originStarted, "Origin request did not start")

	waiterContext, cancelWaiter := context.WithCancel(context.Background())
	waiterResult := make(chan requestResult, 1)
	go requestInBackground(transport, waiterContext, waiterResult)
	waitForMissWaiters(t, transport, 1)
	cancelWaiter()

	result := waitForRequestResult(t, waiterResult)
	if !errors.Is(result.err, context.Canceled) {
		t.Fatalf("waiter error: got %v, want %v", result.err, context.Canceled)
	}

	close(releaseOrigin)
	if result := waitForRequestResult(t, leaderResult); result.err != nil {
		t.Fatal(result.err)
	}
}

type observedResponse struct {
	body        string
	cacheStatus string
}

type requestResult struct {
	response observedResponse
	err      error
}

func performRequest(
	t *testing.T,
	transport http.RoundTripper,
	method string,
) observedResponse {
	t.Helper()

	result := performRequestWithContext(transport, context.Background(), method)
	if result.err != nil {
		t.Fatal(result.err)
	}
	return result.response
}

func performRequestWithContext(
	transport http.RoundTripper,
	ctx context.Context,
	method string,
) requestResult {
	req, err := http.NewRequestWithContext(
		ctx,
		method,
		"http://origin.example/object",
		nil,
	)
	if err != nil {
		return requestResult{err: err}
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		return requestResult{err: err}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return requestResult{err: err}
	}

	return requestResult{
		response: observedResponse{
			body:        string(body),
			cacheStatus: resp.Header.Get(cacheStatusHeader),
		},
	}
}

func requestInBackground(
	transport http.RoundTripper,
	ctx context.Context,
	results chan<- requestResult,
) {
	results <- performRequestWithContext(transport, ctx, http.MethodGet)
}

func originResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func waitForMissWaiters(
	t *testing.T,
	transport *cachingTransport,
	want int,
) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	const key = "http://origin.example/object"
	for {
		transport.misses.mu.Lock()
		flight := transport.misses.flights[key]
		got := 0
		if flight != nil {
			got = flight.waiters
		}
		transport.misses.mu.Unlock()

		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("miss waiters: got %d, want at least %d", got, want)
		}
		runtime.Gosched()
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()

	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func waitForRequestResult(
	t *testing.T,
	results <-chan requestResult,
) requestResult {
	t.Helper()

	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("request did not finish")
		return requestResult{}
	}
}
