package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequestStatsMiddleware(t *testing.T) {
	stats := &requestStats{}
	handler := stats.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if got := stats.snapshot().InFlight; got != 1 {
			t.Fatalf("in-flight requests = %d, want 1", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	got := stats.snapshot()
	if got.Total != 1 || got.InFlight != 0 || got.PeakInFlight != 1 {
		t.Fatalf("request statistics = %+v", got)
	}
}

func TestReadRuntimeStats(t *testing.T) {
	stats := readRuntimeStats()
	if stats.RSSBytes <= 0 {
		t.Fatalf("RSS bytes = %d, want a positive value", stats.RSSBytes)
	}
	if stats.Goroutines <= 0 {
		t.Fatalf("goroutines = %d, want a positive value", stats.Goroutines)
	}
}
