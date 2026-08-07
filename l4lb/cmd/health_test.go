package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"testing"

	"github.com/yzp0n/ncdn/l4lb/l4lbdrv"
)

type recordingDestinationUpdater struct {
	updates []l4lbdrv.DestinationEntries
	err     error
}

type healthRoundTripFunc func(*http.Request) (*http.Response, error)

func (f healthRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func (u *recordingDestinationUpdater) UpdateDestinations(
	destinations l4lbdrv.DestinationEntries,
) error {
	if u.err != nil {
		return u.err
	}
	u.updates = append(u.updates, append(
		l4lbdrv.DestinationEntries(nil),
		destinations...,
	))
	return nil
}

func TestHealthCheckerRemovesAndRestoresDestination(t *testing.T) {
	destinations := testDestinations()
	updater := &recordingDestinationUpdater{}
	probeResults := map[netip.Addr]error{}
	checker, err := newHealthChecker(
		updater,
		destinations,
		func(_ context.Context, destination l4lbdrv.DestinationEntry) error {
			return probeResults[destination.IPv6Addr]
		},
		2,
		2,
	)
	if err != nil {
		t.Fatal(err)
	}

	probeResults[destinations[1].IPv6Addr] = errors.New("unavailable")
	if err := checker.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(updater.updates) != 0 {
		t.Fatalf("updated destinations after one failure: %v", updater.updates)
	}

	if err := checker.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertDestinationIPs(t, updater.updates[0],
		destinations[0].IPv6Addr,
		destinations[2].IPv6Addr,
	)

	probeResults[destinations[1].IPv6Addr] = nil
	if err := checker.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(updater.updates) != 1 {
		t.Fatalf("restored destination after one success: %v", updater.updates)
	}

	if err := checker.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertDestinationIPs(t, updater.updates[1],
		destinations[0].IPv6Addr,
		destinations[1].IPv6Addr,
		destinations[2].IPv6Addr,
	)
}

func TestHealthCheckerSupportsNoHealthyDestinations(t *testing.T) {
	destinations := testDestinations()
	updater := &recordingDestinationUpdater{}
	probeErr := errors.New("unavailable")
	checker, err := newHealthChecker(
		updater,
		destinations,
		func(context.Context, l4lbdrv.DestinationEntry) error {
			return probeErr
		},
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := checker.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertDestinationIPs(t, updater.updates[0], destinations[0].IPv6Addr)
}

func TestHealthCheckerRetriesFailedDestinationUpdate(t *testing.T) {
	destinations := testDestinations()
	updater := &recordingDestinationUpdater{err: errors.New("map update failed")}
	checker, err := newHealthChecker(
		updater,
		destinations,
		func(_ context.Context, destination l4lbdrv.DestinationEntry) error {
			if destination.IPv6Addr == destinations[1].IPv6Addr {
				return errors.New("unavailable")
			}
			return nil
		},
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := checker.check(context.Background()); err == nil {
		t.Fatal("expected destination update to fail")
	}
	updater.err = nil
	if err := checker.check(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertDestinationIPs(t, updater.updates[0],
		destinations[0].IPv6Addr,
		destinations[2].IPv6Addr,
	)
}

func TestHTTPBackendProbeUsesUnderlayAddressAndStatusEndpoint(t *testing.T) {
	destination := testDestinations()[1]
	client := &http.Client{Transport: healthRoundTripFunc(
		func(req *http.Request) (*http.Response, error) {
			if got, want := req.URL.String(),
				"http://[2001:db8::10]:8889/statusz"; got != want {
				t.Fatalf("health check URL: got %q, want %q", got, want)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("{}")),
				Request:    req,
			}, nil
		},
	)}

	if err := httpBackendProbe(client, 8889)(context.Background(), destination); err != nil {
		t.Fatal(err)
	}
}

func testDestinations() l4lbdrv.DestinationEntries {
	return l4lbdrv.DestinationEntries{
		{
			IPv6Addr:     netip.MustParseAddr("2001:db8::20"),
			HardwareAddr: net.HardwareAddr{0, 0, 0, 0, 0, 0x20},
		},
		{
			IPv6Addr:     netip.MustParseAddr("2001:db8::10"),
			HardwareAddr: net.HardwareAddr{0, 0, 0, 0, 0, 0x10},
		},
		{
			IPv6Addr:     netip.MustParseAddr("2001:db8::11"),
			HardwareAddr: net.HardwareAddr{0, 0, 0, 0, 0, 0x11},
		},
	}
}

func assertDestinationIPs(
	t *testing.T,
	destinations l4lbdrv.DestinationEntries,
	want ...netip.Addr,
) {
	t.Helper()
	if len(destinations) != len(want) {
		t.Fatalf("destination count: got %d, want %d", len(destinations), len(want))
	}
	for i := range want {
		if destinations[i].IPv6Addr != want[i] {
			t.Fatalf(
				"destination %d: got %s, want %s",
				i,
				destinations[i].IPv6Addr,
				want[i],
			)
		}
	}
}
