package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	now := time.Now()
	if entry, ok := t.cache.getFreshVariantAt(key, req, now); ok {
		return responseFromCacheAt(req, entry, "HIT", now), nil
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

		now := time.Now()
		if entry, ok := t.cache.getFreshVariantAt(key, req, now); ok {
			return responseFromCacheAt(req, entry, "HIT", now), nil
		}

		// The completed response was not cacheable. Fetch a response for this
		// request without starting another round of waiting.
		return t.fetchFromOrigin(req, key, nil)
	}

	return t.fillCache(req, key, flight)
}

func (t *cachingTransport) fillCache(
	req *http.Request,
	key string,
	flight *missFlight,
) (resp *http.Response, err error) {
	backgroundRefresh := false
	defer func() {
		if !backgroundRefresh {
			t.misses.finish(key, flight, err)
		}
	}()

	// The cache may have been filled between the first lookup and acquire.
	now := time.Now()
	if entry, ok := t.cache.getFreshVariantAt(key, req, now); ok {
		return responseFromCacheAt(req, entry, "HIT", now), nil
	}

	var stale *cacheEntry
	if entry, ok := t.cache.peekVariant(key, req); ok {
		stale = &entry
	}
	if stale != nil && stale.servesStaleWithin(time.Now(), stale.staleWhileRevalidate) {
		backgroundRefresh = true
		refreshReq := req.Clone(context.Background())
		staleEntry := *stale
		go func() {
			_, refreshErr := t.fetchFromOrigin(refreshReq, key, &staleEntry)
			t.misses.finish(key, flight, refreshErr)
		}()
		return responseFromCacheWithStatus(req, *stale, "STALE"), nil
	}
	resp, err = t.fetchFromOrigin(req, key, stale)
	if stale != nil && stale.servesStaleWithin(time.Now(), stale.staleIfError) &&
		(err != nil || resp.StatusCode >= 500) {
		return responseFromCacheWithStatus(req, *stale, "STALE-IF-ERROR"), nil
	}
	return resp, err
}

func (t *cachingTransport) fetchFromOrigin(
	req *http.Request,
	key string,
	stale *cacheEntry,
) (*http.Response, error) {
	originReq := req
	if stale != nil {
		originReq = req.Clone(req.Context())
		if etag := stale.header.Get("ETag"); etag != "" {
			originReq.Header.Set("If-None-Match", etag)
		} else if lastModified := stale.header.Get("Last-Modified"); lastModified != "" {
			originReq.Header.Set("If-Modified-Since", lastModified)
		}
	}

	resp, err := t.base.RoundTrip(originReq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotModified && stale != nil {
		return t.revalidatedResponse(req, key, *stale, resp), nil
	}
	setCacheStatus(resp, "MISS")
	responseReceivedAt := time.Now()

	if !isCacheableResponse(resp) {
		return resp, nil
	}
	vary, ok := responseVary(resp)
	if !ok {
		return resp, nil
	}

	header := resp.Header.Clone()
	header.Del(cacheStatusHeader)
	entry := cacheEntry{
		statusCode:           resp.StatusCode,
		status:               formatHTTPStatus(resp.StatusCode),
		header:               header,
		freshnessLifetime:    freshnessLifetime(resp, t.ttl, responseReceivedAt),
		freshnessLifetimeSet: true,
		initialAge:           responseInitialAge(resp, responseReceivedAt),
		vary:                 vary,
		varyValues:           requestVaryValues(req, vary),
		staleWhileRevalidate: staleControl(resp, "stale-while-revalidate"),
		staleIfError:         staleControl(resp, "stale-if-error"),
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

func (t *cachingTransport) revalidatedResponse(
	req *http.Request,
	key string,
	stale cacheEntry,
	validation *http.Response,
) *http.Response {
	now := time.Now()
	updated := cloneCacheEntry(stale)
	for name, values := range validation.Header {
		updated.header.Del(name)
		for _, value := range values {
			updated.header.Add(name, value)
		}
	}
	updated.header.Del(cacheStatusHeader)
	updated.initialAge = responseInitialAge(validation, now)
	updated.freshnessLifetime = freshnessLifetime(validation, stale.freshnessLifetime, now)
	updated.freshnessLifetimeSet = true
	updated.staleWhileRevalidate = staleControl(&http.Response{Header: updated.header}, "stale-while-revalidate")
	updated.staleIfError = staleControl(&http.Response{Header: updated.header}, "stale-if-error")
	t.cache.set(key, updated, t.ttl)

	result := responseFromCache(req, updated)
	result.Header.Set(cacheStatusHeader, "REVALIDATED")
	return result
}

func isCacheableResponse(resp *http.Response) bool {
	if resp.StatusCode != http.StatusOK {
		return false
	}
	if len(resp.Header.Values("Set-Cookie")) > 0 {
		return false
	}

	for _, value := range resp.Header.Values("Cache-Control") {
		for _, directive := range strings.Split(value, ",") {
			name, _, _ := strings.Cut(directive, "=")
			switch strings.ToLower(strings.TrimSpace(name)) {
			case "no-store", "private":
				return false
			}
		}
	}

	return true
}

func responseVary(resp *http.Response) ([]string, bool) {
	var fields []string
	seen := make(map[string]struct{})
	for _, value := range resp.Header.Values("Vary") {
		for _, part := range strings.Split(value, ",") {
			name := strings.TrimSpace(part)
			if name == "" {
				continue
			}
			if name == "*" {
				return nil, false
			}
			name = http.CanonicalHeaderKey(name)
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			fields = append(fields, name)
		}
	}
	return fields, true
}

func requestVaryValues(req *http.Request, fields []string) http.Header {
	values := make(http.Header)
	for _, field := range fields {
		for _, value := range req.Header.Values(field) {
			values.Add(field, value)
		}
	}
	return values
}

func responseFromCache(req *http.Request, entry cacheEntry) *http.Response {
	return responseFromCacheAt(req, entry, "HIT", time.Now())
}

func responseFromCacheWithStatus(req *http.Request, entry cacheEntry, status string) *http.Response {
	return responseFromCacheAt(req, entry, status, time.Now())
}

func responseFromCacheAt(req *http.Request, entry cacheEntry, status string, now time.Time) *http.Response {
	header := entry.header.Clone()
	header.Set(cacheStatusHeader, status)
	header.Set("Age", strconv.FormatInt(int64(entry.currentAge(now)/time.Second), 10))
	statusText := entry.status
	if statusText == "" {
		statusText = formatHTTPStatus(entry.statusCode)
	}

	return &http.Response{
		StatusCode:    entry.statusCode,
		Status:        statusText,
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(entry.body)),
		ContentLength: int64(len(entry.body)),
		Request:       req,
	}
}

func staleControl(resp *http.Response, name string) time.Duration {
	if hasCacheDirective(resp, "must-revalidate") ||
		hasCacheDirective(resp, "proxy-revalidate") ||
		hasCacheDirective(resp, "no-cache") {
		return 0
	}
	directives := cacheControlDirectives(resp.Header.Values("Cache-Control"))
	return directives[name]
}

// freshnessLifetime follows RFC 9111's shared-cache precedence: s-maxage,
// max-age, Expires minus Date, then the configured fallback TTL.
func freshnessLifetime(resp *http.Response, fallback time.Duration, receivedAt time.Time) time.Duration {
	directives := cacheControlDirectives(resp.Header.Values("Cache-Control"))
	if value, ok := directives["s-maxage"]; ok {
		return value
	}
	if value, ok := directives["max-age"]; ok {
		return value
	}
	if expires, err := http.ParseTime(resp.Header.Get("Expires")); err == nil {
		base := receivedAt
		if date, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
			base = date
		}
		return expires.Sub(base)
	}
	return fallback
}

func responseInitialAge(resp *http.Response, receivedAt time.Time) time.Duration {
	var age time.Duration
	if seconds, err := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("Age")), 10, 64); err == nil && seconds > 0 {
		age = time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		if apparentAge := receivedAt.Sub(date); apparentAge > age {
			age = apparentAge
		}
	}
	return age
}

func cacheControlDirectives(values []string) map[string]time.Duration {
	directives := make(map[string]time.Duration)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name, argument, hasArgument := strings.Cut(strings.TrimSpace(part), "=")
			if !hasArgument {
				continue
			}
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "s-maxage" && name != "max-age" &&
				name != "stale-while-revalidate" && name != "stale-if-error" {
				continue
			}
			seconds, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(argument), "\""), 10, 64)
			if err != nil || seconds < 0 {
				directives[name] = 0
				continue
			}
			if _, exists := directives[name]; !exists {
				directives[name] = time.Duration(seconds) * time.Second
			}
		}
	}
	return directives
}

func hasCacheDirective(resp *http.Response, wanted string) bool {
	for _, value := range resp.Header.Values("Cache-Control") {
		for _, part := range strings.Split(value, ",") {
			name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
			if strings.EqualFold(strings.TrimSpace(name), wanted) {
				return true
			}
		}
	}
	return false
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
