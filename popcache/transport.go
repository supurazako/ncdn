package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"
)

const cacheStatusHeader = "X-NCDN-Cache"

type cachingTransport struct {
	base  http.RoundTripper
	cache *memoryCache
	ttl   time.Duration
}

func newCachingTransport(
	base http.RoundTripper,
	cache *memoryCache,
	ttl time.Duration,
) *cachingTransport {
	if base == nil {
		base = http.DefaultTransport
	}

	return &cachingTransport{
		base:  base,
		cache: cache,
		ttl:   ttl,
	}
}

func (t *cachingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet {
		return t.base.RoundTrip(req)
	}

	key := req.URL.String()
	if entry, ok := t.cache.get(key); ok {
		entry.header.Set(cacheStatusHeader, "HIT")

		return &http.Response{
			StatusCode:    entry.statusCode,
			Status:        formatHTTPStatus(entry.statusCode),
			Header:        entry.header,
			Body:          io.NopCloser(bytes.NewReader(entry.body)),
			ContentLength: int64(len(entry.body)),
			Request:       req,
		}, nil
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))

	if resp.StatusCode == http.StatusOK {
		header := resp.Header.Clone()
		header.Del(cacheStatusHeader)

		t.cache.set(
			key,
			cacheEntry{
				statusCode: resp.StatusCode,
				header:     header,
				body:       body,
			},
			t.ttl,
		)
	}

	resp.Header.Set(cacheStatusHeader, "MISS")
	return resp, nil
}

func formatHTTPStatus(statusCode int) string {
	statusText := http.StatusText(statusCode)
	if statusText == "" {
		return fmt.Sprintf("%d", statusCode)
	}

	return fmt.Sprintf("%d %s", statusCode, statusText)
}
