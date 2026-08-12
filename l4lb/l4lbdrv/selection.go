package l4lbdrv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sort"
)

type SelectionAlgorithm string

const (
	SelectionAlgorithmModulo     SelectionAlgorithm = "modulo"
	SelectionAlgorithmRendezvous SelectionAlgorithm = "rendezvous"
	SelectionAlgorithmMaglev     SelectionAlgorithm = "maglev"
)

func ParseSelectionAlgorithm(value string) (SelectionAlgorithm, error) {
	algorithm := SelectionAlgorithm(value)
	switch algorithm {
	case SelectionAlgorithmModulo,
		SelectionAlgorithmRendezvous,
		SelectionAlgorithmMaglev:
		return algorithm, nil
	default:
		return "", fmt.Errorf(
			"invalid selection algorithm %q (want modulo, rendezvous, or maglev)",
			value,
		)
	}
}

func (algorithm SelectionAlgorithm) normalized() (SelectionAlgorithm, error) {
	if algorithm == "" {
		return SelectionAlgorithmModulo, nil
	}
	return ParseSelectionAlgorithm(string(algorithm))
}

func (algorithm SelectionAlgorithm) bpfValue() uint32 {
	switch algorithm {
	case SelectionAlgorithmRendezvous:
		return SELECTION_ALGORITHM_RENDEZVOUS
	case SelectionAlgorithmMaglev:
		return SELECTION_ALGORITHM_MAGLEV
	default:
		return SELECTION_ALGORITHM_MODULO
	}
}

func buildMaglevLookup(destinations DestinationEntries) ([]uint32, error) {
	if len(destinations) > DESTINATIONS_SIZE {
		return nil, fmt.Errorf(
			"too many Maglev destinations: %d > %d",
			len(destinations),
			DESTINATIONS_SIZE,
		)
	}
	table := make([]uint32, MAGLEV_LOOKUP_SIZE)
	if len(destinations) == 0 {
		return table, nil
	}

	type backend struct {
		index   uint32
		address [16]byte
	}
	backends := make([]backend, len(destinations))
	seen := make(map[[16]byte]struct{}, len(destinations))
	for index, destination := range destinations {
		if !destination.IPv6Addr.Is6() {
			return nil, fmt.Errorf(
				"Maglev destination must contain an IPv6 address: %s",
				destination.IPv6Addr,
			)
		}
		address := destination.IPv6Addr.As16()
		if _, ok := seen[address]; ok {
			return nil, fmt.Errorf("duplicate Maglev destination ID: %s", destination.IPv6Addr)
		}
		seen[address] = struct{}{}
		backends[index] = backend{
			index:   uint32(index + 1),
			address: address,
		}
	}
	sort.Slice(backends, func(i, j int) bool {
		return bytes.Compare(backends[i].address[:], backends[j].address[:]) < 0
	})

	offsets := make([]uint32, len(backends))
	skips := make([]uint32, len(backends))
	next := make([]uint32, len(destinations))
	for index, backend := range backends {
		offsets[index] = stableHash32(backend.address[:], 0) % MAGLEV_LOOKUP_SIZE
		skips[index] = stableHash32(backend.address[:], 0x9e3779b9)%(MAGLEV_LOOKUP_SIZE-1) + 1
	}

	filled := 0
	for filled < MAGLEV_LOOKUP_SIZE {
		for backendIndex, backend := range backends {
			candidate := (offsets[backendIndex] + next[backendIndex]*skips[backendIndex]) % MAGLEV_LOOKUP_SIZE
			for table[candidate] != 0 {
				next[backendIndex]++
				candidate = (offsets[backendIndex] + next[backendIndex]*skips[backendIndex]) % MAGLEV_LOOKUP_SIZE
			}
			table[candidate] = backend.index
			next[backendIndex]++
			filled++
			if filled == MAGLEV_LOOKUP_SIZE {
				break
			}
		}
	}
	return table, nil
}

func buildRendezvousLookup(destinations DestinationEntries) ([]uint32, error) {
	if len(destinations) > DESTINATIONS_SIZE {
		return nil, fmt.Errorf(
			"too many Rendezvous destinations: %d > %d",
			len(destinations),
			DESTINATIONS_SIZE,
		)
	}
	table := make([]uint32, MAGLEV_LOOKUP_SIZE)
	if len(destinations) == 0 {
		return table, nil
	}

	addresses := make([][16]byte, len(destinations))
	seen := make(map[[16]byte]struct{}, len(destinations))
	for index, destination := range destinations {
		if !destination.IPv6Addr.Is6() {
			return nil, fmt.Errorf(
				"Rendezvous destination must contain an IPv6 address: %s",
				destination.IPv6Addr,
			)
		}
		address := destination.IPv6Addr.As16()
		if _, ok := seen[address]; ok {
			return nil, fmt.Errorf("duplicate Rendezvous destination ID: %s", destination.IPv6Addr)
		}
		seen[address] = struct{}{}
		addresses[index] = address
	}

	for slot := range table {
		selected := 0
		highestScore := uint32(0)
		for index, address := range addresses {
			score := stableHash32(address[:], uint32(slot)^0x9e3779b9)
			if index == 0 || score > highestScore ||
				(score == highestScore && bytes.Compare(address[:], addresses[selected][:]) < 0) {
				selected = index
				highestScore = score
			}
		}
		table[slot] = uint32(selected + 1)
	}
	return table, nil
}

func stableHash32(data []byte, seed uint32) uint32 {
	hasher := fnv.New32a()
	var seedBytes [4]byte
	binary.LittleEndian.PutUint32(seedBytes[:], seed)
	_, _ = hasher.Write(seedBytes[:])
	_, _ = hasher.Write(data)

	// FNV-1a alone has visible correlation when consecutive lookup-table slots
	// are used as seeds. Apply an avalanche step so every input bit can affect
	// every output bit before comparing Rendezvous scores.
	value := hasher.Sum32()
	value ^= value >> 16
	value *= 0x85ebca6b
	value ^= value >> 13
	value *= 0xc2b2ae35
	value ^= value >> 16
	return value
}
