package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yzp0n/ncdn/l4lb/l4lbdrv"
)

var lbBin = flag.String("lbBin", "c/lb.o", "Path to XDP lb binary")
var xdpcapHookPath = flag.String("xdpcapHookPath", "/sys/fs/bpf/xdpcap_hook", "Path to XDPCap hook")
var xdpif = flag.String("interface", "net0", "Interface to attach lb prog to")
var vip4 = flag.String("vip", "192.0.2.10", "IPv4 VIP address to load balance")
var vip6 = flag.String("vip6", "2001:db8:100::10", "IPv6 VIP address to load balance")
var deststr = flag.String("dests", "", "Comma separated list of destination IPv6 and MAC addresses. (Example: 2001:db8::10;00:00:5e:00:53:01,)")
var underlayMTU = flag.Uint("underlayMTU", 0, "Required MTU of the IPv6 path from the L4LB to cache nodes")
var healthCheckEnabled = flag.Bool("healthCheckEnabled", true, "Enable cache destination health checks")
var healthCheckInterval = flag.Duration("healthCheckInterval", time.Second, "Interval between cache destination health checks")
var healthCheckTimeout = flag.Duration("healthCheckTimeout", 300*time.Millisecond, "Timeout for each cache destination health check")
var healthCheckFailures = flag.Int("healthCheckFailures", 3, "Consecutive health check failures before removing a cache destination")
var healthCheckSuccesses = flag.Int("healthCheckSuccesses", 2, "Consecutive health check successes before restoring a cache destination")
var healthCheckPort = flag.Uint("healthCheckPort", 8889, "Cache destination health check port")

func parseDest(deststr string) ([]l4lbdrv.DestinationEntry, error) {
	commas := strings.Split(deststr, ",")
	dests := make([]l4lbdrv.DestinationEntry, 0, len(commas))
	for _, c := range commas {
		if c == "" {
			continue
		}

		parts := strings.Split(c, ";")
		if len(parts) != 2 {
			return nil, fmt.Errorf("Invalid destination entry: %s", c)
		}
		ip6, err := netip.ParseAddr(parts[0])
		if err != nil || !ip6.Is6() {
			return nil, fmt.Errorf("Invalid destination IPv6 address: %s", parts[0])
		}

		mac, err := net.ParseMAC(parts[1])
		if err != nil {
			return nil, fmt.Errorf("Invalid MAC address: %s", parts[1])
		}

		dests = append(dests, l4lbdrv.DestinationEntry{
			IPv6Addr:     ip6,
			HardwareAddr: mac,
		})
	}
	log.Printf("dests: %+v", dests)
	return dests, nil
}

func main() {
	flag.Parse()
	if *underlayMTU > 65535 {
		log.Fatalf("Invalid underlay MTU: %d", *underlayMTU)
	}
	if *healthCheckEnabled && *healthCheckInterval <= 0 {
		log.Fatalf("Invalid health check interval: %s", *healthCheckInterval)
	}
	if *healthCheckEnabled && *healthCheckTimeout <= 0 {
		log.Fatalf("Invalid health check timeout: %s", *healthCheckTimeout)
	}
	if *healthCheckPort > 65535 {
		log.Fatalf("Invalid health check port: %d", *healthCheckPort)
	}
	if *healthCheckEnabled && *healthCheckFailures <= 0 {
		log.Fatalf("Invalid health check failure threshold: %d", *healthCheckFailures)
	}
	if *healthCheckEnabled && *healthCheckSuccesses <= 0 {
		log.Fatalf("Invalid health check success threshold: %d", *healthCheckSuccesses)
	}

	dests, err := parseDest(*deststr)
	if err != nil {
		log.Fatalf("Failed to parse dest string: %v", err)
	}

	cfg := &l4lbdrv.Config{
		BinPath:        *lbBin,
		XdpCapHookPath: *xdpcapHookPath,
		InterfaceName:  *xdpif,
		UnderlayMTU:    uint32(*underlayMTU),
		VIP4:           netip.MustParseAddr(*vip4),
		VIP6:           netip.MustParseAddr(*vip6),
		Dests:          dests,
	}
	lb, err := l4lbdrv.New(cfg)
	if err != nil {
		log.Panicf("Failed to create l4lb instance: %v", err)
	}
	slog.Info("L4LB started.")
	defer lb.Close()
	var checker *healthChecker
	var healthCheckC <-chan time.Time
	if *healthCheckEnabled {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		defer transport.CloseIdleConnections()
		checker, err = newHealthChecker(
			lb,
			dests,
			httpBackendProbe(&http.Client{
				Transport: transport,
				Timeout:   *healthCheckTimeout,
			}, uint16(*healthCheckPort)),
			*healthCheckFailures,
			*healthCheckSuccesses,
		)
		if err != nil {
			log.Fatalf("Invalid health check configuration: %v", err)
		}
		healthTicker := time.NewTicker(*healthCheckInterval)
		defer healthTicker.Stop()
		healthCheckC = healthTicker.C
		if err := checker.check(context.Background()); err != nil {
			slog.Error("Failed to update cache destinations", "err", err)
		}
	} else {
		slog.Info("Cache destination health checks disabled.")
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	counterTicker := time.NewTicker(time.Second)
	defer counterTicker.Stop()
	for {
		select {
		case <-counterTicker.C:
			if err := lb.DumpCounters(); err != nil {
				slog.Error("Failed to dump counters", slog.String("err", err.Error()))
			}
			continue

		case <-healthCheckC:
			if err := checker.check(context.Background()); err != nil {
				slog.Error("Failed to update cache destinations", "err", err)
			}
			continue

		case <-done:
			break
		}
		break
	}
	slog.Info("Shutting down.")
}
