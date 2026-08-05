package l4lbdrv

import (
	"bytes"
	"encoding/binary"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

var pcapWriter *pcapgo.Writer

func DumpDebugPcap(b []byte) {
	if pcapWriter == nil {
		f, err := os.Create("/tmp/debug.pcap")
		if err != nil {
			panic(err)
		}

		pcapWriter = pcapgo.NewWriter(f)
		if err := pcapWriter.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
			panic(err)
		}
	}
	if err := pcapWriter.WritePacket(gopacket.CaptureInfo{
		Timestamp:      time.Now(),
		CaptureLength:  len(b),
		Length:         len(b),
		InterfaceIndex: 0,
	}, b); err != nil {
		panic(err)
	}
}

func TestL4LBIPv4InIPv6(t *testing.T) {
	vip4 := netip.MustParseAddr("192.0.2.10")
	lbMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xfe}

	cfg := &Config{
		BinPath:     "../c/lb.o",
		UnderlayMTU: 1500,
		VIP4:        vip4,
		VIP6:        netip.MustParseAddr("2001:db8:100::10"),
		Dests: []DestinationEntry{
			{
				IPv6Addr:     netip.MustParseAddr("2001:db8::fe"),
				HardwareAddr: lbMAC,
			},
			{
				IPv6Addr:     netip.MustParseAddr("2001:db8::10"),
				HardwareAddr: []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0x10},
			},
		},
	}
	lb, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create L4LB: %v", err)
	}
	defer lb.Close()

	eth := &layers.Ethernet{
		SrcMAC:       []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xff},
		DstMAC:       lbMAC,
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip4 := &layers.IPv4{
		SrcIP:    netip.MustParseAddr("10.0.0.123").AsSlice(),
		DstIP:    vip4.AsSlice(),
		Version:  4,
		TTL:      64,
		Protocol: layers.IPProtocolTCP,
	}
	tcp := &layers.TCP{
		SrcPort: 12345,
		DstPort: 80,
		SYN:     true,
	}
	if err := tcp.SetNetworkLayerForChecksum(ip4); err != nil {
		t.Errorf("SetNetworkLayerForChecksum: %v", err)
	}

	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf,
		gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		eth, ip4, tcp); err != nil {
		t.Fatalf("Failed to serialize packet: %v", err)
	}

	if err := lb.bindings.ResetStatCounters(); err != nil {
		t.Fatalf("Failed to ResetStatCounters: %v", err)
	}
	DumpDebugPcap(buf.Bytes())
	retval, out, err := lb.bindings.LBMain.Test(buf.Bytes())
	if err != nil {
		t.Fatalf("Failed to execute XDP program: %v", err)
	}
	if retval != XDP_TX {
		t.Fatalf("Expected XDP_TX but got %s", XdpRetValToString(retval))
	}
	DumpDebugPcap(out)

	packet := gopacket.NewPacket(out, layers.LayerTypeEthernet, gopacket.Default)
	outer, ok := packet.Layer(layers.LayerTypeIPv6).(*layers.IPv6)
	if !ok {
		t.Fatalf("Expected an outer IPv6 header: %s", packet.Dump())
	}
	if got, want := netip.AddrFrom16([16]byte(outer.SrcIP)), cfg.Dests[0].IPv6Addr; got != want {
		t.Errorf("Outer source = %s, want %s", got, want)
	}
	if got, want := netip.AddrFrom16([16]byte(outer.DstIP)), cfg.Dests[1].IPv6Addr; got != want {
		t.Errorf("Outer destination = %s, want %s", got, want)
	}
	if outer.NextHeader != layers.IPProtocolIPv4 {
		t.Errorf("Outer next header = %s, want IPv4", outer.NextHeader)
	}

	cnt, err := lb.bindings.ReadStatCountersAggregate()
	if err != nil {
		t.Fatalf("Failed to ReadStatCountersAggregate: %v", err)
	}
	if cnt.RxPacketTotal != 1 || cnt.Ipv4PacketTotal != 1 {
		t.Errorf("Unexpected counters: %s", cnt)
	}
}

func TestL4LBIPv6InIPv6(t *testing.T) {
	vip6 := netip.MustParseAddr("2001:db8:100::10")
	lbMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xfe}
	backendMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0x10}

	cfg := &Config{
		BinPath:     "../c/lb.o",
		UnderlayMTU: 1500,
		VIP4:        netip.MustParseAddr("192.0.2.10"),
		VIP6:        vip6,
		Dests: []DestinationEntry{
			{
				IPv6Addr:     netip.MustParseAddr("2001:db8::fe"),
				HardwareAddr: lbMAC,
			},
			{
				IPv6Addr:     netip.MustParseAddr("2001:db8::10"),
				HardwareAddr: backendMAC,
			},
		},
	}
	lb, err := New(cfg)
	if err != nil {
		t.Fatalf("Failed to create L4LB: %v", err)
	}
	defer lb.Close()

	eth := &layers.Ethernet{
		SrcMAC:       []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xff},
		DstMAC:       lbMAC,
		EthernetType: layers.EthernetTypeIPv6,
	}
	ip6 := &layers.IPv6{
		SrcIP:      netip.MustParseAddr("2001:db8:200::123").AsSlice(),
		DstIP:      vip6.AsSlice(),
		Version:    6,
		HopLimit:   64,
		NextHeader: layers.IPProtocolTCP,
	}
	tcp := &layers.TCP{
		SrcPort: 12345,
		DstPort: 80,
		SYN:     true,
	}
	if err := tcp.SetNetworkLayerForChecksum(ip6); err != nil {
		t.Fatalf("SetNetworkLayerForChecksum: %v", err)
	}

	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf,
		gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		eth, ip6, tcp); err != nil {
		t.Fatalf("Failed to serialize packet: %v", err)
	}

	if err := lb.bindings.ResetStatCounters(); err != nil {
		t.Fatalf("Failed to ResetStatCounters: %v", err)
	}
	retval, out, err := lb.bindings.LBMain.Test(buf.Bytes())
	if err != nil {
		t.Fatalf("Failed to execute XDP program: %v", err)
	}
	if retval != XDP_TX {
		t.Fatalf("Expected XDP_TX but got %s", XdpRetValToString(retval))
	}

	packet := gopacket.NewPacket(out, layers.LayerTypeEthernet, gopacket.Default)
	outer, ok := packet.Layer(layers.LayerTypeIPv6).(*layers.IPv6)
	if !ok {
		t.Fatalf("Expected an outer IPv6 header: %s", packet.Dump())
	}
	if got, want := netip.AddrFrom16([16]byte(outer.SrcIP)), cfg.Dests[0].IPv6Addr; got != want {
		t.Errorf("Outer source = %s, want %s", got, want)
	}
	if got, want := netip.AddrFrom16([16]byte(outer.DstIP)), cfg.Dests[1].IPv6Addr; got != want {
		t.Errorf("Outer destination = %s, want %s", got, want)
	}
	if outer.NextHeader != layers.IPProtocolIPv6 {
		t.Errorf("Outer next header = %s, want IPv6", outer.NextHeader)
	}

	cnt, err := lb.bindings.ReadStatCountersAggregate()
	if err != nil {
		t.Fatalf("Failed to ReadStatCountersAggregate: %v", err)
	}
	if cnt.RxPacketTotal != 1 || cnt.Ipv6PacketTotal != 1 {
		t.Errorf("Unexpected counters: %s", cnt)
	}
}

func TestL4LBUnsupportedVIPProtocolPolicy(t *testing.T) {
	vip4 := netip.MustParseAddr("192.0.2.10")
	vip6 := netip.MustParseAddr("2001:db8:100::10")
	lbMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xfe}
	lb := newTestLB(t, vip4, vip6, lbMAC)
	defer lb.Close()

	tests := []struct {
		name string
		src  netip.Addr
		dst  netip.Addr
		want uint32
	}{
		{
			name: "IPv4 UDP to VIP is dropped",
			src:  netip.MustParseAddr("10.0.0.123"),
			dst:  vip4,
			want: XDP_DROP,
		},
		{
			name: "IPv6 UDP to VIP is dropped",
			src:  netip.MustParseAddr("2001:db8:200::123"),
			dst:  vip6,
			want: XDP_DROP,
		},
		{
			name: "IPv4 UDP outside VIP is passed",
			src:  netip.MustParseAddr("10.0.0.123"),
			dst:  netip.MustParseAddr("192.0.2.20"),
			want: XDP_PASS,
		},
		{
			name: "IPv6 UDP outside VIP is passed",
			src:  netip.MustParseAddr("2001:db8:200::123"),
			dst:  netip.MustParseAddr("2001:db8:100::20"),
			want: XDP_PASS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := lb.bindings.ResetStatCounters(); err != nil {
				t.Fatalf("Failed to ResetStatCounters: %v", err)
			}
			retval := runUDPPacket(t, lb, tt.src, tt.dst, lbMAC)
			if retval != tt.want {
				t.Fatalf("XDP action = %s, want %s",
					XdpRetValToString(retval), XdpRetValToString(tt.want))
			}

			cnt, err := lb.bindings.ReadStatCountersAggregate()
			if err != nil {
				t.Fatalf("Failed to ReadStatCountersAggregate: %v", err)
			}
			if tt.want == XDP_DROP && cnt.NonSupportedProtoPacketTotal != 1 {
				t.Errorf("Expected unsupported protocol counter: %s", cnt)
			}
			if tt.want == XDP_PASS &&
				(cnt.NoVipMatchTotal != 1 || cnt.NonSupportedProtoPacketTotal != 0) {
				t.Errorf("Expected only no-VIP-match counter: %s", cnt)
			}
		})
	}
}

func TestL4LBIPv4TooLargeReturnsFragmentationNeeded(t *testing.T) {
	vip4 := netip.MustParseAddr("192.0.2.10")
	clientIP := netip.MustParseAddr("10.0.0.123")
	clientMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xff}
	lbMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xfe}
	// Use a non-default underlay MTU to verify that the configured value reaches
	// the XDP program: 1492 - 40-byte outer IPv6 = 1452-byte inner MTU.
	lb := newTestLBWithUnderlay(t, vip4,
		netip.MustParseAddr("2001:db8:100::10"), lbMAC, 1492)
	defer lb.Close()

	eth := &layers.Ethernet{
		SrcMAC:       clientMAC,
		DstMAC:       lbMAC,
		EthernetType: layers.EthernetTypeIPv4,
	}
	ip4 := &layers.IPv4{
		SrcIP:    clientIP.AsSlice(),
		DstIP:    vip4.AsSlice(),
		Version:  4,
		TTL:      64,
		Flags:    layers.IPv4DontFragment,
		Protocol: layers.IPProtocolTCP,
	}
	tcp := &layers.TCP{SrcPort: 12345, DstPort: 80, ACK: true}
	if err := tcp.SetNetworkLayerForChecksum(ip4); err != nil {
		t.Fatalf("SetNetworkLayerForChecksum: %v", err)
	}

	// 20-byte IPv4 + 20-byte TCP + 1421-byte data = a 1461-byte inner
	// packet, which exceeds the configured 1452-byte inner MTU.
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf,
		gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		eth, ip4, tcp, gopacket.Payload(make([]byte, 1421))); err != nil {
		t.Fatalf("Failed to serialize packet: %v", err)
	}
	original := append([]byte(nil), buf.Bytes()...)

	if err := lb.bindings.ResetStatCounters(); err != nil {
		t.Fatalf("Failed to ResetStatCounters: %v", err)
	}
	retval, out, err := lb.bindings.LBMain.Test(original)
	if err != nil {
		t.Fatalf("Failed to execute XDP program: %v", err)
	}
	if retval != XDP_TX {
		t.Fatalf("Expected XDP_TX but got %s", XdpRetValToString(retval))
	}
	if got, want := len(out), 70; got != want {
		t.Fatalf("ICMPv4 frame length = %d, want %d", got, want)
	}
	if !bytes.Equal(out[0:6], clientMAC) || !bytes.Equal(out[6:12], lbMAC) {
		t.Errorf("Ethernet addresses were not swapped: %x", out[:12])
	}

	packet := gopacket.NewPacket(out, layers.LayerTypeEthernet, gopacket.Default)
	replyIP, ok := packet.Layer(layers.LayerTypeIPv4).(*layers.IPv4)
	if !ok {
		t.Fatalf("Expected IPv4 reply: %s", packet.Dump())
	}
	if got, want := netip.AddrFrom4([4]byte(replyIP.SrcIP)), vip4; got != want {
		t.Errorf("IPv4 reply source = %s, want %s", got, want)
	}
	if got, want := netip.AddrFrom4([4]byte(replyIP.DstIP)), clientIP; got != want {
		t.Errorf("IPv4 reply destination = %s, want %s", got, want)
	}
	icmp, ok := packet.Layer(layers.LayerTypeICMPv4).(*layers.ICMPv4)
	if !ok {
		t.Fatalf("Expected ICMPv4 reply: %s", packet.Dump())
	}
	if got, want := icmp.TypeCode, layers.CreateICMPv4TypeCode(
		layers.ICMPv4TypeDestinationUnreachable, 4); got != want {
		t.Errorf("ICMPv4 type/code = %v, want %v", got, want)
	}
	if got, want := binary.BigEndian.Uint16(out[40:42]), uint16(1452); got != want {
		t.Errorf("ICMPv4 next-hop MTU = %d, want %d", got, want)
	}
	if !bytes.Equal(out[42:70], original[14:42]) {
		t.Error("ICMPv4 response did not quote the original IPv4 header and first 8 bytes")
	}
	if got := internetChecksum(out[14:34]); got != 0 {
		t.Errorf("Invalid IPv4 header checksum: %#x", got)
	}
	if got := internetChecksum(out[34:70]); got != 0 {
		t.Errorf("Invalid ICMPv4 checksum: %#x", got)
	}

	cnt, err := lb.bindings.ReadStatCountersAggregate()
	if err != nil {
		t.Fatalf("Failed to ReadStatCountersAggregate: %v", err)
	}
	if cnt.MtuExceededPacketTotal != 1 || cnt.Icmpv4FragNeededTotal != 1 {
		t.Errorf("Unexpected counters: %s", cnt)
	}
}

func TestL4LBIPv6TooLargeReturnsPacketTooBig(t *testing.T) {
	vip6 := netip.MustParseAddr("2001:db8:100::10")
	clientIP := netip.MustParseAddr("2001:db8:200::123")
	clientMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xff}
	lbMAC := []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xfe}
	lb := newTestLB(t, netip.MustParseAddr("192.0.2.10"), vip6, lbMAC)
	defer lb.Close()

	eth := &layers.Ethernet{
		SrcMAC:       clientMAC,
		DstMAC:       lbMAC,
		EthernetType: layers.EthernetTypeIPv6,
	}
	ip6 := &layers.IPv6{
		SrcIP:      clientIP.AsSlice(),
		DstIP:      vip6.AsSlice(),
		Version:    6,
		HopLimit:   64,
		NextHeader: layers.IPProtocolTCP,
	}
	tcp := &layers.TCP{SrcPort: 12345, DstPort: 80, ACK: true}
	if err := tcp.SetNetworkLayerForChecksum(ip6); err != nil {
		t.Fatalf("SetNetworkLayerForChecksum: %v", err)
	}

	// 40-byte IPv6 + 20-byte TCP + 1401-byte data = a 1461-byte inner
	// packet, one byte larger than the IPv6-underlay budget.
	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf,
		gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		eth, ip6, tcp, gopacket.Payload(make([]byte, 1401))); err != nil {
		t.Fatalf("Failed to serialize packet: %v", err)
	}
	original := append([]byte(nil), buf.Bytes()...)

	if err := lb.bindings.ResetStatCounters(); err != nil {
		t.Fatalf("Failed to ResetStatCounters: %v", err)
	}
	retval, out, err := lb.bindings.LBMain.Test(original)
	if err != nil {
		t.Fatalf("Failed to execute XDP program: %v", err)
	}
	if retval != XDP_TX {
		t.Fatalf("Expected XDP_TX but got %s", XdpRetValToString(retval))
	}
	if got, want := len(out), 1294; got != want {
		t.Fatalf("ICMPv6 frame length = %d, want %d", got, want)
	}
	if !bytes.Equal(out[0:6], clientMAC) || !bytes.Equal(out[6:12], lbMAC) {
		t.Errorf("Ethernet addresses were not swapped: %x", out[:12])
	}

	packet := gopacket.NewPacket(out, layers.LayerTypeEthernet, gopacket.Default)
	replyIP, ok := packet.Layer(layers.LayerTypeIPv6).(*layers.IPv6)
	if !ok {
		t.Fatalf("Expected IPv6 reply: %s", packet.Dump())
	}
	if got, want := netip.AddrFrom16([16]byte(replyIP.SrcIP)), vip6; got != want {
		t.Errorf("IPv6 reply source = %s, want %s", got, want)
	}
	if got, want := netip.AddrFrom16([16]byte(replyIP.DstIP)), clientIP; got != want {
		t.Errorf("IPv6 reply destination = %s, want %s", got, want)
	}
	icmp, ok := packet.Layer(layers.LayerTypeICMPv6).(*layers.ICMPv6)
	if !ok {
		t.Fatalf("Expected ICMPv6 reply: %s", packet.Dump())
	}
	if got, want := icmp.TypeCode, layers.CreateICMPv6TypeCode(
		layers.ICMPv6TypePacketTooBig, 0); got != want {
		t.Errorf("ICMPv6 type/code = %v, want %v", got, want)
	}
	if got, want := binary.BigEndian.Uint32(out[58:62]), uint32(1460); got != want {
		t.Errorf("ICMPv6 MTU = %d, want %d", got, want)
	}
	if !bytes.Equal(out[62:1294], original[14:14+1232]) {
		t.Error("ICMPv6 response did not quote the expected part of the original packet")
	}
	pseudo := make([]byte, 0, 40+1240)
	pseudo = append(pseudo, replyIP.SrcIP...)
	pseudo = append(pseudo, replyIP.DstIP...)
	pseudo = binary.BigEndian.AppendUint32(pseudo, 1240)
	pseudo = append(pseudo, 0, 0, 0, byte(layers.IPProtocolICMPv6))
	pseudo = append(pseudo, out[54:1294]...)
	if got := internetChecksum(pseudo); got != 0 {
		withZeroChecksum := append([]byte(nil), pseudo...)
		withZeroChecksum[42] = 0
		withZeroChecksum[43] = 0
		t.Errorf("Invalid ICMPv6 checksum: validation=%#x packet=%#x expected=%#x",
			got, icmp.Checksum, internetChecksum(withZeroChecksum))
	}

	cnt, err := lb.bindings.ReadStatCountersAggregate()
	if err != nil {
		t.Fatalf("Failed to ReadStatCountersAggregate: %v", err)
	}
	if cnt.MtuExceededPacketTotal != 1 || cnt.Icmpv6PacketTooBigTotal != 1 {
		t.Errorf("Unexpected counters: %s", cnt)
	}
}

func newTestLB(t *testing.T, vip4, vip6 netip.Addr, lbMAC []byte) *L4LB {
	return newTestLBWithUnderlay(t, vip4, vip6, lbMAC, 1500)
}

func runUDPPacket(t *testing.T, lb *L4LB, src, dst netip.Addr, lbMAC []byte) uint32 {
	t.Helper()
	eth := &layers.Ethernet{
		SrcMAC: []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0xff},
		DstMAC: lbMAC,
	}
	udp := &layers.UDP{SrcPort: 12345, DstPort: 8889}
	var networkLayer gopacket.NetworkLayer
	var serializableNetworkLayer gopacket.SerializableLayer
	if src.Is4() && dst.Is4() {
		eth.EthernetType = layers.EthernetTypeIPv4
		ip4 := &layers.IPv4{
			SrcIP: src.AsSlice(), DstIP: dst.AsSlice(), Version: 4,
			TTL: 64, Protocol: layers.IPProtocolUDP,
		}
		networkLayer = ip4
		serializableNetworkLayer = ip4
	} else if src.Is6() && dst.Is6() {
		eth.EthernetType = layers.EthernetTypeIPv6
		ip6 := &layers.IPv6{
			SrcIP: src.AsSlice(), DstIP: dst.AsSlice(), Version: 6,
			HopLimit: 64, NextHeader: layers.IPProtocolUDP,
		}
		networkLayer = ip6
		serializableNetworkLayer = ip6
	} else {
		t.Fatalf("source and destination address families differ: %s, %s", src, dst)
	}
	if err := udp.SetNetworkLayerForChecksum(networkLayer); err != nil {
		t.Fatalf("SetNetworkLayerForChecksum: %v", err)
	}

	buf := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buf,
		gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true},
		eth, serializableNetworkLayer, udp); err != nil {
		t.Fatalf("Failed to serialize UDP packet: %v", err)
	}
	retval, _, err := lb.bindings.LBMain.Test(buf.Bytes())
	if err != nil {
		t.Fatalf("Failed to execute XDP program: %v", err)
	}
	return retval
}

func newTestLBWithUnderlay(t *testing.T, vip4, vip6 netip.Addr, lbMAC []byte, underlayMTU uint32) *L4LB {
	t.Helper()
	lb, err := New(&Config{
		BinPath:     "../c/lb.o",
		UnderlayMTU: underlayMTU,
		VIP4:        vip4,
		VIP6:        vip6,
		Dests: []DestinationEntry{
			{
				IPv6Addr:     netip.MustParseAddr("2001:db8::fe"),
				HardwareAddr: lbMAC,
			},
			{
				IPv6Addr:     netip.MustParseAddr("2001:db8::10"),
				HardwareAddr: []byte{0x00, 0x00, 0x5e, 0x00, 0x53, 0x10},
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create L4LB: %v", err)
	}
	return lb
}

// internetChecksum returns zero when data contains a valid Internet checksum.
func internetChecksum(data []byte) uint16 {
	var sum uint32
	for len(data) >= 2 {
		sum += uint32(binary.BigEndian.Uint16(data[:2]))
		data = data[2:]
	}
	if len(data) == 1 {
		sum += uint32(data[0]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}
