package l4lbdrv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/cilium/ebpf"
	"github.com/vishvananda/netlink"
	"go.uber.org/multierr"
)

type Config struct {
	BinPath        string
	InterfaceName  string
	XDPMode        XDPMode
	XdpCapHookPath string
	UnderlayMTU    uint32

	VIP4               netip.Addr
	VIP6               netip.Addr
	UDPPort            uint16
	Dests              DestinationEntries
	SelectionAlgorithm SelectionAlgorithm
}

type L4LB struct {
	cfg *Config
	mu  sync.Mutex

	bindings     *Bindings
	linkAttacher *LinkAttacher
}

func New(cfg *Config) (*L4LB, error) {
	algorithm, err := cfg.SelectionAlgorithm.normalized()
	if err != nil {
		return nil, err
	}
	cfg.SelectionAlgorithm = algorithm
	if _, err := cfg.InnerMTU(); err != nil {
		return nil, err
	}
	if err := PrepSystemForXDP(); err != nil {
		return nil, fmt.Errorf("Failed to prep system for XDP: %w", err)
	}
	aBinPath, err := filepath.Abs(cfg.BinPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to get absolute path for %s: %w", cfg.BinPath, err)
	}
	var aXdpcapHookPath string
	if cfg.XdpCapHookPath != "" {
		aXdpcapHookPath, err = filepath.Abs(cfg.XdpCapHookPath)
		if err != nil {
			return nil, fmt.Errorf("Failed to get absolute path for %s: %w", cfg.XdpCapHookPath, err)
		}
	}
	bindings, err := BindBalancer(aBinPath, aXdpcapHookPath)
	if err != nil {
		return nil, fmt.Errorf("Failed to bind balancer: %w", err)
	}

	lb := &L4LB{
		cfg:      cfg,
		bindings: bindings,
	}

	var link netlink.Link
	if cfg.InterfaceName == "" {
		slog.Info("No interface name provided, skipping link attachment.")
	} else {
		l, err := netlink.LinkByName(cfg.InterfaceName)
		if err != nil {
			return nil, fmt.Errorf("Failed to find interface %q: %w", cfg.InterfaceName, err)
		}
		link = l
	}
	if link != nil {
		a, err := AttachToLink(link, bindings.LBMain.FD(), cfg.XDPMode)
		if err != nil {
			return nil, multierr.Combine(err, bindings.Close())
		}
		lb.linkAttacher = a
	}
	if err := lb.Sync(); err != nil {
		return nil, fmt.Errorf("Initial map sync failed: %w", err)
	}

	return lb, nil
}

var hostOrder = binary.LittleEndian

const outerIPv6HeaderLen = uint32(40)

// InnerMTU returns the largest original client IP packet that can be wrapped
// in one outer IPv6 header without exceeding the configured underlay MTU.
func (cfg *Config) InnerMTU() (uint32, error) {
	// An IPv6-in-IPv6 deployment needs room for IPv6's minimum 1280-byte MTU
	// plus the 40-byte outer IPv6 header.
	if cfg.UnderlayMTU < 1320 || cfg.UnderlayMTU > 65535 {
		return 0, fmt.Errorf("underlay MTU must be between 1320 and 65535: %d", cfg.UnderlayMTU)
	}
	return cfg.UnderlayMTU - outerIPv6HeaderLen, nil
}

func IPToUint32(ip netip.Addr) (uint32, error) {
	if !ip.Is4() {
		return 0, fmt.Errorf("given IP is not an IPv4 address: %s", ip)
	}

	ip4 := ip.As4()
	return hostOrder.Uint32(ip4[:]), nil
}

func (lb *L4LB) Sync() error {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if len(lb.cfg.Dests) < 2 {
		return fmt.Errorf("at least one cache destination is required")
	}
	return lb.syncDestinationsLocked(lb.cfg.Dests)
}

// UpdateDestinations replaces the active cache destinations without reloading
// the XDP program. Entry zero must remain the L4LB itself; an empty cache set is
// represented by a slice containing only that entry.
func (lb *L4LB) UpdateDestinations(dests DestinationEntries) error {
	lb.mu.Lock()
	defer lb.mu.Unlock()

	if len(dests) < 1 {
		return fmt.Errorf("L4LB destination is required")
	}
	if len(dests) > DESTINATIONS_SIZE+1 {
		return fmt.Errorf(
			"too many cache destinations: %d > %d",
			len(dests)-1,
			DESTINATIONS_SIZE,
		)
	}
	if dests[0].IPv6Addr != lb.cfg.Dests[0].IPv6Addr ||
		!bytes.Equal(dests[0].HardwareAddr, lb.cfg.Dests[0].HardwareAddr) {
		return fmt.Errorf("L4LB destination at index zero must not change")
	}
	if err := lb.syncDestinationsLocked(dests); err != nil {
		return err
	}

	lb.cfg.Dests = cloneDestinationEntries(dests)
	return nil
}

func (lb *L4LB) syncDestinationsLocked(dests DestinationEntries) error {
	if len(dests) < 1 {
		return fmt.Errorf("L4LB destination is required")
	}
	if len(dests) > DESTINATIONS_SIZE+1 {
		return fmt.Errorf(
			"too many cache destinations: %d > %d",
			len(dests)-1,
			DESTINATIONS_SIZE,
		)
	}

	vip4, err := IPToUint32(lb.cfg.VIP4)
	if err != nil {
		return fmt.Errorf("IPv4 VIP: %w", err)
	}
	if !lb.cfg.VIP6.Is6() {
		return fmt.Errorf("IPv6 VIP is not an IPv6 address: %s", lb.cfg.VIP6)
	}
	vip6 := lb.cfg.VIP6.As16()
	innerMTU, err := lb.cfg.InnerMTU()
	if err != nil {
		return err
	}

	keys := make([]uint32, len(dests))
	for i := range keys {
		keys[i] = uint32(i)
	}

	_, err = lb.bindings.DestinationArray.BatchUpdate(keys, dests, &ebpf.BatchOptions{})
	if err != nil {
		return fmt.Errorf("Failed to update DestinationArray: %w", err)
	}

	if lb.cfg.SelectionAlgorithm == SelectionAlgorithmRendezvous ||
		lb.cfg.SelectionAlgorithm == SelectionAlgorithmMaglev {
		var table []uint32
		var err error
		if lb.cfg.SelectionAlgorithm == SelectionAlgorithmRendezvous {
			table, err = buildRendezvousLookup(dests[1:])
		} else {
			table, err = buildMaglevLookup(dests[1:])
		}
		if err != nil {
			return fmt.Errorf("Failed to build %s lookup table: %w",
				lb.cfg.SelectionAlgorithm, err)
		}
		lookupKeys := make([]uint32, len(table))
		for index := range lookupKeys {
			lookupKeys[index] = uint32(index)
		}
		updated, err := lb.bindings.SelectionLookupMap.BatchUpdate(
			lookupKeys,
			table,
			&ebpf.BatchOptions{},
		)
		if err != nil {
			return fmt.Errorf(
				"Failed to update SelectionLookupMap after %d entries: %w",
				updated,
				err,
			)
		}
	}

	err = lb.bindings.ConfigMap.Update(uint32(0), &LbConfig{
		Vip4Address:        vip4,
		Vip6Address:        vip6,
		SrcIp6Address:      dests[0].IPv6Addr.As16(),
		SrcMacAddress:      [6]uint8(dests[0].HardwareAddr),
		UdpDestPort:        lb.cfg.UDPPort,
		NumDests:           uint32(len(dests) - 1),
		InnerMtu:           innerMTU,
		SelectionAlgorithm: lb.cfg.SelectionAlgorithm.bpfValue(),
	}, 0)
	if err != nil {
		return fmt.Errorf("Failed to update ConfigMap: %w", err)
	}

	inlineConfig := inlineLbConfig{
		Base: LbConfig{
			Vip4Address:        vip4,
			Vip6Address:        vip6,
			SrcIp6Address:      dests[0].IPv6Addr.As16(),
			SrcMacAddress:      [6]uint8(dests[0].HardwareAddr),
			UdpDestPort:        lb.cfg.UDPPort,
			NumDests:           uint32(min(len(dests)-1, inlineDestinationsSize)),
			InnerMtu:           innerMTU,
			SelectionAlgorithm: lb.cfg.SelectionAlgorithm.bpfValue(),
		},
	}
	for index, destination := range dests[1:min(len(dests), inlineDestinationsSize+1)] {
		ip6 := destination.IPv6Addr.As16()
		copy(inlineConfig.DestIP6Addresses[index*16:(index+1)*16], ip6[:])
		copy(inlineConfig.DestMACAddresses[index*6:(index+1)*6], destination.HardwareAddr)
	}
	if err := lb.bindings.InlineConfigMap.Update(uint32(0), &inlineConfig, 0); err != nil {
		return fmt.Errorf("Failed to update InlineConfigMap: %w", err)
	}

	return nil
}

func cloneDestinationEntries(dests DestinationEntries) DestinationEntries {
	cloned := make(DestinationEntries, len(dests))
	for i, dest := range dests {
		cloned[i] = dest
		cloned[i].HardwareAddr = append([]byte(nil), dest.HardwareAddr...)
	}
	return cloned
}

func (lb *L4LB) Close() error {
	return lb.bindings.Close()
}

func (lb *L4LB) DumpCounters() error {
	cnt, err := lb.bindings.ReadStatCountersAggregate()
	if err != nil {
		return err
	}

	slog.Info(cnt.String())

	return nil
}

// `PrepSystemForXDP` configures RLIMIT_MEMLOCK to ensure enough room to
// allocate eBPF programs and maps on older Linux systems.
func PrepSystemForXDP() error {
	const RLIMIT_MEMLOCK = 8
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(RLIMIT_MEMLOCK, &rlim); err != nil {
		return fmt.Errorf("Failed to Getrlimit(RLIMIT_MEMLOCK): %v", err)
	}
	slog.Info("Getrlimit(RLIMIT_MEMLOCK)", "Cur", rlim.Cur, "Max", rlim.Max)

	rlim.Cur = math.MaxUint64
	rlim.Max = math.MaxUint64
	if err := syscall.Setrlimit(RLIMIT_MEMLOCK, &rlim); err != nil {
		return fmt.Errorf("Failed to Setrlimit(RLIMIT_MEMLOCK): %v", err)
	}
	slog.Info("Setrlimit(RLIMIT_MEMLOCK)", "Cur", rlim.Cur, "Max", rlim.Max)

	return nil
}
