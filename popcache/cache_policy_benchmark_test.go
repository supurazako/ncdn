package main

import (
	"container/list"
	"math/rand"
	"testing"
)

type cachePolicyTrace struct {
	keys     []int
	capacity int
}

func newCachePolicyTrace() cachePolicyTrace {
	const (
		requests = 200_000
		keys     = 10_000
		capacity = 1_000
	)
	return newZipfTrace(1.15, requests, keys, capacity, 1)
}

func newZipfTrace(exponent float64, requests, keys, capacity int, seed int64) cachePolicyTrace {
	random := rand.New(rand.NewSource(seed))
	distribution := rand.NewZipf(random, exponent, 1, uint64(keys-1))
	trace := make([]int, requests)
	for index := range trace {
		trace[index] = int(distribution.Uint64())
	}
	return cachePolicyTrace{keys: trace, capacity: capacity}
}

func TestCachePolicyHitRateAcrossTraces(t *testing.T) {
	const (
		requests = 100_000
		keys     = 10_000
	)
	cases := []struct {
		name     string
		exponent float64
		capacity int
		seed     int64
	}{
		{name: "broad-capacity100", exponent: 1.05, capacity: 100, seed: 1},
		{name: "broad-capacity1000", exponent: 1.05, capacity: 1_000, seed: 1},
		{name: "moderate-capacity100", exponent: 1.15, capacity: 100, seed: 2},
		{name: "moderate-capacity1000", exponent: 1.15, capacity: 1_000, seed: 2},
		{name: "hot-capacity100", exponent: 1.5, capacity: 100, seed: 3},
		{name: "hot-capacity1000", exponent: 1.5, capacity: 1_000, seed: 3},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			trace := newZipfTrace(test.exponent, requests, keys, test.capacity, test.seed)
			lruHits := simulateLRU(trace.keys, trace.capacity)
			clockHits := simulateClock(trace.keys, trace.capacity)
			t.Logf("LRU hit ratio: %.4f, CLOCK hit ratio: %.4f, difference: %.4f", float64(lruHits)/requests, float64(clockHits)/requests, float64(clockHits-lruHits)/requests)
		})
	}
}

func BenchmarkCachePolicyTrace(b *testing.B) {
	trace := newCachePolicyTrace()
	for _, policy := range []struct {
		name string
		run  func([]int, int) int
	}{
		{name: "LRU", run: simulateLRU},
		{name: "CLOCK", run: simulateClock},
	} {
		b.Run(policy.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			hits := 0
			for iteration := 0; iteration < b.N; iteration++ {
				hits = policy.run(trace.keys, trace.capacity)
			}
			b.ReportMetric(float64(hits)/float64(len(trace.keys)), "hit_ratio")
		})
	}
}

type lruPolicyEntry struct {
	key int
}

func simulateLRU(trace []int, capacity int) int {
	cache := make(map[int]*list.Element, capacity)
	entries := list.New()
	hits := 0
	for _, key := range trace {
		if element, ok := cache[key]; ok {
			hits++
			entries.MoveToFront(element)
			continue
		}
		if entries.Len() == capacity {
			oldest := entries.Back()
			delete(cache, oldest.Value.(lruPolicyEntry).key)
			entries.Remove(oldest)
		}
		cache[key] = entries.PushFront(lruPolicyEntry{key: key})
	}
	return hits
}

type clockPolicyEntry struct {
	key      int
	occupied bool
	refer    bool
}

func simulateClock(trace []int, capacity int) int {
	cache := make(map[int]int, capacity)
	entries := make([]clockPolicyEntry, capacity)
	hand := 0
	hits := 0
	for _, key := range trace {
		if index, ok := cache[key]; ok {
			hits++
			entries[index].refer = true
			continue
		}
		for entries[hand].occupied && entries[hand].refer {
			entries[hand].refer = false
			hand = (hand + 1) % capacity
		}
		if entries[hand].occupied {
			delete(cache, entries[hand].key)
		}
		entries[hand] = clockPolicyEntry{key: key, occupied: true, refer: true}
		cache[key] = hand
		hand = (hand + 1) % capacity
	}
	return hits
}
