package main

import (
	"flag"
	"log"
	"net"
	"net/netip"
	"os"
	"time"

	"github.com/google/gopacket"
	"github.com/google/gopacket/layers"
	"github.com/google/gopacket/pcapgo"
)

func main() {
	family := flag.String("family", "ipv4", "IP family: ipv4 or ipv6")
	srcIP := flag.String("src-ip", "", "Source IP address")
	dstIP := flag.String("dst-ip", "", "Destination IP address")
	srcMAC := flag.String("src-mac", "", "Source MAC address")
	dstMAC := flag.String("dst-mac", "", "Destination MAC address")
	output := flag.String("output", "", "Output pcap path")
	flag.Parse()

	srcAddr, err := netip.ParseAddr(*srcIP)
	if err != nil {
		log.Fatalf("invalid source IP: %v", err)
	}
	dstAddr, err := netip.ParseAddr(*dstIP)
	if err != nil {
		log.Fatalf("invalid destination IP: %v", err)
	}
	sourceMAC, err := net.ParseMAC(*srcMAC)
	if err != nil {
		log.Fatalf("invalid source MAC: %v", err)
	}
	destinationMAC, err := net.ParseMAC(*dstMAC)
	if err != nil {
		log.Fatalf("invalid destination MAC: %v", err)
	}
	if *output == "" {
		log.Fatal("output path is required")
	}

	ethernetType := layers.EthernetTypeIPv4
	if *family == "ipv6" {
		ethernetType = layers.EthernetTypeIPv6
	} else if *family != "ipv4" {
		log.Fatalf("invalid family: %s", *family)
	}
	ethernet := &layers.Ethernet{
		SrcMAC:       sourceMAC,
		DstMAC:       destinationMAC,
		EthernetType: ethernetType,
	}
	tcp := &layers.TCP{SrcPort: 40000, DstPort: 5201, SYN: true, Window: 65535}
	options := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	buffer := gopacket.NewSerializeBuffer()
	if *family == "ipv4" {
		ip := &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolTCP, SrcIP: srcAddr.AsSlice(), DstIP: dstAddr.AsSlice()}
		tcp.SetNetworkLayerForChecksum(ip)
		err = gopacket.SerializeLayers(buffer, options, ethernet, ip, tcp)
	} else {
		ip := &layers.IPv6{Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolTCP, SrcIP: srcAddr.AsSlice(), DstIP: dstAddr.AsSlice()}
		tcp.SetNetworkLayerForChecksum(ip)
		err = gopacket.SerializeLayers(buffer, options, ethernet, ip, tcp)
	}
	if err != nil {
		log.Fatalf("serialize packet: %v", err)
	}

	file, err := os.Create(*output)
	if err != nil {
		log.Fatalf("create pcap: %v", err)
	}
	defer file.Close()
	writer := pcapgo.NewWriter(file)
	if err := writer.WriteFileHeader(65535, layers.LinkTypeEthernet); err != nil {
		log.Fatalf("write pcap header: %v", err)
	}
	packet := buffer.Bytes()
	if err := writer.WritePacket(gopacket.CaptureInfo{Timestamp: time.Unix(0, 0), CaptureLength: len(packet), Length: len(packet)}, packet); err != nil {
		log.Fatalf("write packet: %v", err)
	}
}
