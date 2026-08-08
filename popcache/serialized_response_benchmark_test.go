package main

import (
	"bytes"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func BenchmarkSerializedCacheResponse(b *testing.B) {
	entry := cacheEntry{
		statusCode:           http.StatusOK,
		status:               "200 OK",
		header:               http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"max-age=60"}},
		body:                 []byte(`{"ok":true}`),
		storedAt:             time.Now(),
		freshnessLifetime:    time.Minute,
		freshnessLifetimeSet: true,
	}
	request := httptestRequestForBenchmark()
	b.Run("http-response", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			response := responseFromCacheAt(request, entry, "HIT", time.Now())
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
	})
	b.Run("serialized", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = io.Copy(io.Discard, serializeCacheResponse(entry, "HIT", time.Now()))
		}
	})
}

func serializeCacheResponse(entry cacheEntry, cacheStatus string, now time.Time) io.Reader {
	var response bytes.Buffer
	response.WriteString("HTTP/1.1 ")
	response.WriteString(entry.status)
	response.WriteString("\r\n")
	for name, values := range entry.header {
		for _, value := range values {
			response.WriteString(name)
			response.WriteString(": ")
			response.WriteString(value)
			response.WriteString("\r\n")
		}
	}
	response.WriteString(cacheStatusHeader)
	response.WriteString(": ")
	response.WriteString(cacheStatus)
	response.WriteString("\r\nAge: ")
	response.WriteString(strconv.FormatInt(int64(entry.currentAge(now)/time.Second), 10))
	response.WriteString("\r\nContent-Length: ")
	response.WriteString(strconv.Itoa(len(entry.body)))
	response.WriteString("\r\n\r\n")
	_, _ = response.Write(entry.body)
	return &response
}

func httptestRequestForBenchmark() *http.Request {
	return &http.Request{Method: http.MethodGet, Header: make(http.Header)}
}
