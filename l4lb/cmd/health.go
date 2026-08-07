package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/yzp0n/ncdn/l4lb/l4lbdrv"
)

type destinationUpdater interface {
	UpdateDestinations(l4lbdrv.DestinationEntries) error
}

type backendProbe func(context.Context, l4lbdrv.DestinationEntry) error

type backendHealth struct {
	destination          l4lbdrv.DestinationEntry
	healthy              bool
	consecutiveFailures  int
	consecutiveSuccesses int
}

type healthChecker struct {
	lb               destinationUpdater
	lbDestination    l4lbdrv.DestinationEntry
	backends         []backendHealth
	probe            backendProbe
	failureThreshold int
	successThreshold int
	dirty            bool
}

func newHealthChecker(
	lb destinationUpdater,
	destinations l4lbdrv.DestinationEntries,
	probe backendProbe,
	failureThreshold int,
	successThreshold int,
) (*healthChecker, error) {
	if len(destinations) < 2 {
		return nil, fmt.Errorf("at least one cache destination is required")
	}
	if failureThreshold <= 0 {
		return nil, fmt.Errorf("health check failure threshold must be positive")
	}
	if successThreshold <= 0 {
		return nil, fmt.Errorf("health check success threshold must be positive")
	}

	backends := make([]backendHealth, 0, len(destinations)-1)
	for _, destination := range destinations[1:] {
		backends = append(backends, backendHealth{
			destination: destination,
			healthy:     true,
		})
	}

	return &healthChecker{
		lb:               lb,
		lbDestination:    destinations[0],
		backends:         backends,
		probe:            probe,
		failureThreshold: failureThreshold,
		successThreshold: successThreshold,
	}, nil
}

func (c *healthChecker) check(ctx context.Context) error {
	for i := range c.backends {
		backend := &c.backends[i]
		err := c.probe(ctx, backend.destination)
		if err == nil {
			backend.consecutiveFailures = 0
			if backend.healthy {
				backend.consecutiveSuccesses = 0
				continue
			}

			backend.consecutiveSuccesses++
			if backend.consecutiveSuccesses >= c.successThreshold {
				backend.healthy = true
				backend.consecutiveSuccesses = 0
				c.dirty = true
				slog.Info("Cache destination recovered",
					"destination", backend.destination.IPv6Addr)
			}
			continue
		}

		backend.consecutiveSuccesses = 0
		if !backend.healthy {
			backend.consecutiveFailures = 0
			continue
		}

		backend.consecutiveFailures++
		if backend.consecutiveFailures >= c.failureThreshold {
			backend.healthy = false
			backend.consecutiveFailures = 0
			c.dirty = true
			slog.Warn("Cache destination became unhealthy",
				"destination", backend.destination.IPv6Addr,
				"error", err)
		}
	}

	if !c.dirty {
		return nil
	}

	active := make(l4lbdrv.DestinationEntries, 1, len(c.backends)+1)
	active[0] = c.lbDestination
	for _, backend := range c.backends {
		if backend.healthy {
			active = append(active, backend.destination)
		}
	}
	if err := c.lb.UpdateDestinations(active); err != nil {
		return fmt.Errorf("update healthy cache destinations: %w", err)
	}

	c.dirty = false
	slog.Info("Updated healthy cache destinations", "count", len(active)-1)
	return nil
}

func httpBackendProbe(client *http.Client, port uint16) backendProbe {
	return func(ctx context.Context, destination l4lbdrv.DestinationEntry) error {
		host := net.JoinHostPort(destination.IPv6Addr.String(), strconv.Itoa(int(port)))
		req, err := http.NewRequestWithContext(
			ctx,
			http.MethodGet,
			"http://"+host+"/statusz",
			nil,
		)
		if err != nil {
			return err
		}

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("health endpoint returned %s", resp.Status)
		}

		return nil
	}
}
