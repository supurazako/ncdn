package main

import (
	"io"
	"net/http"
	"strconv"
	"time"
)

// cacheHitHandler serves the common, single-variant cache hit before the
// reverse proxy constructs an origin request. Misses and variants continue
// through the existing proxy and caching transport.
type cacheHitHandler struct {
	cache *memoryCache
	next  http.Handler
}

func (h *cacheHitHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet {
		now := time.Now()
		if entry, ok := h.cache.getFreshRequestVariant(req, now); ok {
			writeCachedResponse(w, responseFromCacheAt(req, entry, "HIT", now))
			return
		}
	}
	h.next.ServeHTTP(w, req)
}

func writeCachedResponse(w http.ResponseWriter, resp *http.Response) {
	for name, values := range resp.Header {
		if hopByHopResponseHeader(name) {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
	_ = resp.Body.Close()
}

func hopByHopResponseHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
