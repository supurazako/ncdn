package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type requestStats struct {
	total        atomic.Uint64
	inFlight     atomic.Int64
	peakInFlight atomic.Int64
	logEvery     uint64
}

type requestStatsSnapshot struct {
	Total        uint64 `json:"total"`
	InFlight     int64  `json:"in_flight"`
	PeakInFlight int64  `json:"peak_in_flight"`
}

func (s *requestStats) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedAt := time.Now()
		total := s.total.Add(1)
		inFlight := s.inFlight.Add(1)
		for {
			peak := s.peakInFlight.Load()
			if inFlight <= peak || s.peakInFlight.CompareAndSwap(peak, inFlight) {
				break
			}
		}
		defer s.inFlight.Add(-1)

		shouldLog := s.logEvery > 0 && total%s.logEvery == 0
		if shouldLog {
			defer func() {
				log.Printf(
					"event=access request_total=%d remote=%q method=%q host=%q path=%q duration_ms=%.3f",
					total, r.RemoteAddr, r.Method, r.Host, r.URL.Path,
					float64(time.Since(startedAt).Microseconds())/1000,
				)
			}()
		}
		next.ServeHTTP(w, r)
	})
}

func (s *requestStats) snapshot() requestStatsSnapshot {
	return requestStatsSnapshot{
		Total:        s.total.Load(),
		InFlight:     s.inFlight.Load(),
		PeakInFlight: s.peakInFlight.Load(),
	}
}

type runtimeStatsSnapshot struct {
	RSSBytes                   int64   `json:"rss_bytes"`
	MaxRSSBytes                int64   `json:"max_rss_bytes"`
	HeapAllocBytes             uint64  `json:"heap_alloc_bytes"`
	HeapSysBytes               uint64  `json:"heap_sys_bytes"`
	HeapObjects                uint64  `json:"heap_objects"`
	StackInuseBytes            uint64  `json:"stack_inuse_bytes"`
	Goroutines                 int     `json:"goroutines"`
	GCCycles                   uint32  `json:"gc_cycles"`
	CPUUserSeconds             float64 `json:"cpu_user_seconds"`
	CPUSystemSeconds           float64 `json:"cpu_system_seconds"`
	MaxTemperatureMilliCelsius int64   `json:"max_temperature_millicelsius"`
}

func readRuntimeStats() runtimeStatsSnapshot {
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)

	var usage syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &usage)

	return runtimeStatsSnapshot{
		RSSBytes:                   readRSSBytes(),
		MaxRSSBytes:                usage.Maxrss * 1024,
		HeapAllocBytes:             memory.HeapAlloc,
		HeapSysBytes:               memory.HeapSys,
		HeapObjects:                memory.HeapObjects,
		StackInuseBytes:            memory.StackInuse,
		Goroutines:                 runtime.NumGoroutine(),
		GCCycles:                   memory.NumGC,
		CPUUserSeconds:             timevalSeconds(usage.Utime),
		CPUSystemSeconds:           timevalSeconds(usage.Stime),
		MaxTemperatureMilliCelsius: readMaxTemperatureMilliCelsius(),
	}
}

func readMaxTemperatureMilliCelsius() int64 {
	paths, err := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	if err != nil || len(paths) == 0 {
		return -1
	}
	maximum := int64(-1)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
		if err == nil && value > maximum {
			maximum = value
		}
	}
	return maximum
}

func readRSSBytes() int64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return -1
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return -1
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return -1
	}
	return pages * int64(os.Getpagesize())
}

func timevalSeconds(value syscall.Timeval) float64 {
	return float64(value.Sec) + float64(value.Usec)/1e6
}

func startRuntimeStatsLogger(
	interval time.Duration,
	start time.Time,
	nodeID string,
	requests *requestStats,
	cache *memoryCache,
) {
	if interval == 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		previousRuntime := readRuntimeStats()
		previousAt := time.Now()
		for range ticker.C {
			now := time.Now()
			runtimeStats := readRuntimeStats()
			cpuDelta := runtimeStats.CPUUserSeconds + runtimeStats.CPUSystemSeconds -
				previousRuntime.CPUUserSeconds - previousRuntime.CPUSystemSeconds
			cpuPercent := cpuDelta / now.Sub(previousAt).Seconds() * 100
			requestStats := requests.snapshot()
			cacheStats := cache.stats()
			log.Printf(
				"event=runtime_stats node_id=%q uptime_seconds=%.0f requests_total=%d requests_in_flight=%d requests_peak_in_flight=%d rss_bytes=%d max_rss_bytes=%d heap_alloc_bytes=%d heap_sys_bytes=%d heap_objects=%d stack_inuse_bytes=%d goroutines=%d gc_cycles=%d cpu_percent=%.1f cpu_user_seconds=%.3f cpu_system_seconds=%.3f max_temperature_millicelsius=%d cache_entries=%d cache_used_bytes=%d cache_max_bytes=%d",
				nodeID, time.Since(start).Seconds(), requestStats.Total,
				requestStats.InFlight, requestStats.PeakInFlight,
				runtimeStats.RSSBytes, runtimeStats.MaxRSSBytes,
				runtimeStats.HeapAllocBytes, runtimeStats.HeapSysBytes,
				runtimeStats.HeapObjects, runtimeStats.StackInuseBytes,
				runtimeStats.Goroutines, runtimeStats.GCCycles,
				cpuPercent, runtimeStats.CPUUserSeconds, runtimeStats.CPUSystemSeconds,
				runtimeStats.MaxTemperatureMilliCelsius,
				cacheStats.Entries, cacheStats.UsedBytes, cacheStats.MaxBytes,
			)
			previousRuntime = runtimeStats
			previousAt = now
		}
	}()
}
