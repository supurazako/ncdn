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
	base   http.RoundTripper
	cache  *memoryCache
	ttl    time.Duration
	misses missGroup
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
		return responseFromCache(req, entry), nil
	}

	flight, leader := t.misses.acquire(key)
	if !leader {
		select {
		case <-flight.done:
			if flight.err != nil {
				return nil, flight.err
			}
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}

		if entry, ok := t.cache.get(key); ok {
			return responseFromCache(req, entry), nil
		}

		// The completed response was not cacheable. Fetch a response for this
		// request without starting another round of waiting.
		return t.fetchFromOrigin(req, key)
	}

	return t.fillCache(req, key, flight)
}

func (t *cachingTransport) fillCache(
	req *http.Request,
	key string,
	flight *missFlight,
) (resp *http.Response, err error) {
	defer func() {
		t.misses.finish(key, flight, err)
	}()

	// The cache may have been filled between the first lookup and acquire.
	if entry, ok := t.cache.get(key); ok {
		return responseFromCache(req, entry), nil
	}

	return t.fetchFromOrigin(req, key)
}

func (t *cachingTransport) fetchFromOrigin(
	req *http.Request,
	key string,
) (*http.Response, error) {
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

func responseFromCache(req *http.Request, entry cacheEntry) *http.Response {
	entry.header.Set(cacheStatusHeader, "HIT")

	return &http.Response{
		StatusCode:    entry.statusCode,
		Status:        formatHTTPStatus(entry.statusCode),
		Header:        entry.header,
		Body:          io.NopCloser(bytes.NewReader(entry.body)),
		ContentLength: int64(len(entry.body)),
		Request:       req,
	}
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
