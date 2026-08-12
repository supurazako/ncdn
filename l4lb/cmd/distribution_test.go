package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/yzp0n/ncdn/l4lb/l4lbdrv"
)

type fakeCounterReader struct {
	counters *l4lbdrv.StatCounters
}

func (reader fakeCounterReader) ReadCounters() (*l4lbdrv.StatCounters, error) {
	return reader.counters, nil
}

func TestDistributionHandler(t *testing.T) {
	counters := &l4lbdrv.StatCounters{}
	counters.DestinationPacketTotal[0] = 12
	counters.DestinationPacketTotal[1] = 8
	counters.DestinationByteTotal[0] = 1200
	counters.DestinationByteTotal[1] = 900
	dests := []l4lbdrv.DestinationEntry{
		{IPv6Addr: netip.MustParseAddr("fd00:4::5"), HardwareAddr: net.HardwareAddr{0, 1, 2, 3, 4, 5}},
		{IPv6Addr: netip.MustParseAddr("fd00:4::7"), HardwareAddr: net.HardwareAddr{0, 1, 2, 3, 4, 7}},
		{IPv6Addr: netip.MustParseAddr("fd00:4::8"), HardwareAddr: net.HardwareAddr{0, 1, 2, 3, 4, 8}},
	}
	handler := distributionHandler(
		fakeCounterReader{counters}, dests, []string{"C0", "C1"},
		"192.0.2.10", "2001:db8::10", l4lbdrv.SelectionAlgorithmRendezvous,
	)
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, "/distribution", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("got status %d: %s", response.Code, response.Body.String())
	}
	var snapshot distributionSnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Backends) != 2 || snapshot.Backends[0].ID != "C0" || snapshot.Backends[1].Packets != 8 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.SelectionAlgorithm != "rendezvous" {
		t.Fatalf("unexpected algorithm: %q", snapshot.SelectionAlgorithm)
	}
}

func TestParseBackendNamesUsesFallbacks(t *testing.T) {
	names := parseBackendNames("C0", 2)
	if names[0] != "C0" || names[1] != "edge-1" {
		t.Fatalf("unexpected names: %v", names)
	}
}
