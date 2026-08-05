package main

import "sync"

// missGroup tracks one in-flight Origin request for each cache key.
type missGroup struct {
	mu      sync.Mutex
	flights map[string]*missFlight
}

type missFlight struct {
	done    chan struct{}
	err     error
	waiters int
}

func (g *missGroup) acquire(key string) (*missFlight, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if flight, ok := g.flights[key]; ok {
		flight.waiters++
		return flight, false
	}

	if g.flights == nil {
		g.flights = make(map[string]*missFlight)
	}
	flight := &missFlight{done: make(chan struct{})}
	g.flights[key] = flight
	return flight, true
}

func (g *missGroup) finish(key string, flight *missFlight, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	flight.err = err
	delete(g.flights, key)
	close(flight.done)
}
