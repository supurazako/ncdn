package main

import (
	"io"
	"net/http"
	"strings"
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
		newMemoryCache(),
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
		newMemoryCache(),
		time.Minute,
	)

	performRequest(t, transport, http.MethodPost)
	performRequest(t, transport, http.MethodPost)

	if originRequests != 2 {
		t.Fatalf("origin requests: got %d, want 2", originRequests)
	}
}

type observedResponse struct {
	body        string
	cacheStatus string
}

func performRequest(
	t *testing.T,
	transport http.RoundTripper,
	method string,
) observedResponse {
	t.Helper()

	req, err := http.NewRequest(method, "http://origin.example/object", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	return observedResponse{
		body:        string(body),
		cacheStatus: resp.Header.Get(cacheStatusHeader),
	}
}
