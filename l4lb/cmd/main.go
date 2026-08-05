package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net"
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

	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ticker.C:
			if err := lb.DumpCounters(); err != nil {
				slog.Error("Failed to dump counters", slog.String("err", err.Error()))
			}
			continue

		case <-done:
			break
		}
		break
	}
	slog.Info("Shutting down.")
}
