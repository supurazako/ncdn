package l4lbdrv

import (
	"net"
	"net/netip"
	"testing"
)

func TestParseSelectionAlgorithm(t *testing.T) {
	for _, value := range []string{"modulo", "rendezvous", "maglev"} {
		if got, err := ParseSelectionAlgorithm(value); err != nil || string(got) != value {
			t.Fatalf("ParseSelectionAlgorithm(%q) = %q, %v", value, got, err)
		}
	}
	if _, err := ParseSelectionAlgorithm("random"); err == nil {
		t.Fatal("invalid selection algorithm was accepted")
	}
}

func TestSelectionTablesAreBalanced(t *testing.T) {
	destinations := testSelectionDestinations("2001:db8::10", "2001:db8::11", "2001:db8::12")
	builders := map[string]func(DestinationEntries) ([]uint32, error){
		"rendezvous": buildRendezvousLookup,
		"maglev":     buildMaglevLookup,
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			table, err := build(destinations)
			if err != nil {
				t.Fatal(err)
			}
			counts := make([]int, len(destinations))
			for _, selected := range table {
				if selected == 0 || int(selected) > len(destinations) {
					t.Fatalf("invalid backend index %d", selected)
				}
				counts[selected-1]++
			}
			for index, count := range counts {
				ratio := float64(count) / float64(len(table))
				if ratio < 0.31 || ratio > 0.36 {
					t.Fatalf("backend %d ratio = %.4f, counts=%v", index, ratio, counts)
				}
			}
		})
	}
}

func TestSelectionTablesIgnoreRegistrationOrder(t *testing.T) {
	first := testSelectionDestinations("2001:db8::10", "2001:db8::11", "2001:db8::12")
	second := testSelectionDestinations("2001:db8::12", "2001:db8::10", "2001:db8::11")
	builders := map[string]func(DestinationEntries) ([]uint32, error){
		"rendezvous": buildRendezvousLookup,
		"maglev":     buildMaglevLookup,
	}
	for name, build := range builders {
		t.Run(name, func(t *testing.T) {
			firstTable, err := build(first)
			if err != nil {
				t.Fatal(err)
			}
			secondTable, err := build(second)
			if err != nil {
				t.Fatal(err)
			}
			for slot := range firstTable {
				firstID := first[firstTable[slot]-1].IPv6Addr
				secondID := second[secondTable[slot]-1].IPv6Addr
				if firstID != secondID {
					t.Fatalf("slot %d changed from %s to %s after reordering", slot, firstID, secondID)
				}
			}
		})
	}
}

func TestRendezvousOnlyMovesRemovedBackendSlots(t *testing.T) {
	before := testSelectionDestinations("2001:db8::10", "2001:db8::11", "2001:db8::12")
	after := testSelectionDestinations("2001:db8::10", "2001:db8::12")
	beforeTable, err := buildRendezvousLookup(before)
	if err != nil {
		t.Fatal(err)
	}
	afterTable, err := buildRendezvousLookup(after)
	if err != nil {
		t.Fatal(err)
	}
	removed := netip.MustParseAddr("2001:db8::11")
	for slot := range beforeTable {
		beforeID := before[beforeTable[slot]-1].IPv6Addr
		afterID := after[afterTable[slot]-1].IPv6Addr
		if beforeID != removed && beforeID != afterID {
			t.Fatalf("slot %d unnecessarily moved from %s to %s", slot, beforeID, afterID)
		}
	}
}

func TestMaglevLimitsMovementAfterBackendRemoval(t *testing.T) {
	before := testSelectionDestinations("2001:db8::10", "2001:db8::11", "2001:db8::12")
	after := testSelectionDestinations("2001:db8::10", "2001:db8::12")
	beforeTable, err := buildMaglevLookup(before)
	if err != nil {
		t.Fatal(err)
	}
	afterTable, err := buildMaglevLookup(after)
	if err != nil {
		t.Fatal(err)
	}
	removed := netip.MustParseAddr("2001:db8::11")
	unnecessaryMoves := 0
	for slot := range beforeTable {
		beforeID := before[beforeTable[slot]-1].IPv6Addr
		afterID := after[afterTable[slot]-1].IPv6Addr
		if beforeID != removed && beforeID != afterID {
			unnecessaryMoves++
		}
	}
	ratio := float64(unnecessaryMoves) / float64(len(beforeTable))
	if ratio > 0.01 {
		t.Fatalf("Maglev unnecessarily moved %.2f%% of slots", ratio*100)
	}
}

func TestSelectionTablesRejectDuplicateBackendIDs(t *testing.T) {
	destinations := testSelectionDestinations("2001:db8::10", "2001:db8::10")
	if _, err := buildRendezvousLookup(destinations); err == nil {
		t.Fatal("Rendezvous accepted duplicate backend IDs")
	}
	if _, err := buildMaglevLookup(destinations); err == nil {
		t.Fatal("Maglev accepted duplicate backend IDs")
	}
}

func TestL4LBSelectionAlgorithms(t *testing.T) {
	vip4 := netip.MustParseAddr("192.0.2.10")
	vip6 := netip.MustParseAddr("2001:db8:100::10")
	lbMAC := net.HardwareAddr{0, 0, 0x5e, 0, 0x53, 0xfe}
	backendAddresses := []netip.Addr{
		netip.MustParseAddr("2001:db8::10"),
		netip.MustParseAddr("2001:db8::11"),
	}

	for _, algorithm := range []SelectionAlgorithm{
		SelectionAlgorithmModulo,
		SelectionAlgorithmRendezvous,
		SelectionAlgorithmMaglev,
	} {
		t.Run(string(algorithm), func(t *testing.T) {
			lb, err := New(&Config{
				BinPath:            "../c/lb.o",
				UnderlayMTU:        1500,
				VIP4:               vip4,
				VIP6:               vip6,
				SelectionAlgorithm: algorithm,
				Dests: []DestinationEntry{
					{IPv6Addr: netip.MustParseAddr("2001:db8::fe"), HardwareAddr: lbMAC},
					{IPv6Addr: backendAddresses[0], HardwareAddr: net.HardwareAddr{0, 0, 0x5e, 0, 0x53, 0x10}},
					{IPv6Addr: backendAddresses[1], HardwareAddr: net.HardwareAddr{0, 0, 0x5e, 0, 0x53, 0x11}},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer lb.Close()

			counts := make(map[netip.Addr]int)
			for sourcePort := uint16(10000); sourcePort < 11000; sourcePort++ {
				packet := serializeIPv4TCPPacket(
					t,
					netip.MustParseAddr("198.51.100.20"),
					vip4,
					sourcePort,
					8889,
					lbMAC,
				)
				out := runXDP(t, lb, packet, XDP_TX)
				selected := outerIPv6Destination(t, out)
				counts[selected]++

				// Replaying an identical flow must select the same backend.
				repeated := runXDP(t, lb, packet, XDP_TX)
				if got := outerIPv6Destination(t, repeated); got != selected {
					t.Fatalf("flow changed backend from %s to %s", selected, got)
				}
			}

			for _, backend := range backendAddresses {
				ratio := float64(counts[backend]) / 1000
				if ratio < 0.40 || ratio > 0.60 {
					t.Fatalf("backend %s ratio = %.3f, counts=%v", backend, ratio, counts)
				}
			}
		})
	}
}

func testSelectionDestinations(addresses ...string) DestinationEntries {
	destinations := make(DestinationEntries, len(addresses))
	for index, address := range addresses {
		destinations[index] = DestinationEntry{
			IPv6Addr:     netip.MustParseAddr(address),
			HardwareAddr: net.HardwareAddr{0, 0, 0, 0, 0, byte(index + 1)},
		}
	}
	return destinations
}
