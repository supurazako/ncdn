package main

import (
	"bytes"
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
	responseReceivedAt := time.Now()

	if !isCacheableResponse(resp) {
		return resp, nil
	}

	header := resp.Header.Clone()
	header.Del(cacheStatusHeader)
	entry := cacheEntry{
		statusCode:           resp.StatusCode,
		header:               header,
		freshnessLifetime:    freshnessLifetime(resp, t.ttl, responseReceivedAt),
		freshnessLifetimeSet: true,
		initialAge:           responseInitialAge(resp, responseReceivedAt),
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

func responseFromCache(req *http.Request, entry cacheEntry) *http.Response {
	entry.header.Set(cacheStatusHeader, "HIT")
	entry.header.Set("Age", strconv.FormatInt(int64(entry.currentAge(time.Now())/time.Second), 10))

	return &http.Response{
		StatusCode:    entry.statusCode,
		Status:        formatHTTPStatus(entry.statusCode),
		Header:        entry.header,
		Body:          io.NopCloser(bytes.NewReader(entry.body)),
		ContentLength: int64(len(entry.body)),
		Request:       req,
	}
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
			if name != "s-maxage" && name != "max-age" {
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
