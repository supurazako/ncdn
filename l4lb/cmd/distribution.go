package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/yzp0n/ncdn/l4lb/l4lbdrv"
)

type counterReader interface {
	ReadCounters() (*l4lbdrv.StatCounters, error)
}

type distributionBackend struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

type distributionSnapshot struct {
	Timestamp          time.Time             `json:"timestamp"`
	VIP4               string                `json:"vip4"`
	VIP6               string                `json:"vip6"`
	SelectionAlgorithm string                `json:"selection_algorithm"`
	Backends           []distributionBackend `json:"backends"`
}

func parseBackendNames(raw string, count int) []string {
	provided := strings.Split(raw, ",")
	names := make([]string, count)
	for index := range names {
		if index < len(provided) && strings.TrimSpace(provided[index]) != "" {
			names[index] = strings.TrimSpace(provided[index])
		} else {
			names[index] = fmt.Sprintf("edge-%d", index)
		}
	}
	return names
}

func distributionHandler(
	reader counterReader,
	dests []l4lbdrv.DestinationEntry,
	names []string,
	vip4 string,
	vip6 string,
	algorithm l4lbdrv.SelectionAlgorithm,
) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		counters, err := reader.ReadCounters()
		if err != nil {
			http.Error(w, "failed to read L4LB counters", http.StatusServiceUnavailable)
			return
		}
		backends := make([]distributionBackend, 0, len(dests)-1)
		for index, destination := range dests[1:] {
			backends = append(backends, distributionBackend{
				ID:      names[index],
				Address: destination.IPv6Addr.String(),
				Packets: counters.DestinationPacketTotal[index],
				Bytes:   counters.DestinationByteTotal[index],
			})
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(distributionSnapshot{
			Timestamp:          time.Now(),
			VIP4:               vip4,
			VIP6:               vip6,
			SelectionAlgorithm: string(algorithm),
			Backends:           backends,
		})
	}
}
