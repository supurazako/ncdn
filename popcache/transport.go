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

type readerWithCloser struct {
	io.Reader
	io.Closer
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
	setCacheStatus(resp, "MISS")

	if resp.StatusCode != http.StatusOK {
		return resp, nil
	}

	header := resp.Header.Clone()
	header.Del(cacheStatusHeader)
	entry := cacheEntry{
		statusCode: resp.StatusCode,
		header:     header,
	}
	maxBodyBytes := t.cache.maxCacheableBodyBytes(key, entry)
	if maxBodyBytes < 0 ||
		(resp.ContentLength >= 0 && resp.ContentLength > maxBodyBytes) {
		return resp, nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		_ = resp.Body.Close()
		return nil, err
	}
	if int64(len(body)) > maxBodyBytes {
		resp.Body = readerWithCloser{
			Reader: io.MultiReader(bytes.NewReader(body), resp.Body),
			Closer: resp.Body,
		}
		return resp, nil
	}
	_ = resp.Body.Close()

	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	entry.body = body
	t.cache.set(key, entry, t.ttl)
	return resp, nil
}

func setCacheStatus(resp *http.Response, status string) {
	if resp.Header == nil {
		resp.Header = make(http.Header)
	}
	resp.Header.Set(cacheStatusHeader, status)
}

func formatHTTPStatus(statusCode int) string {
	statusText := http.StatusText(statusCode)
	if statusText == "" {
		return fmt.Sprintf("%d", statusCode)
	}

	return fmt.Sprintf("%d %s", statusCode, statusText)
}
