#include <assert.h>
#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ipv6.h>
#include <netinet/in.h>
#include <netinet/ip.h>
#include <netinet/tcp.h>

#include <bpf/bpf_helpers.h>

#include <stdint.h>
#include <sys/types.h>

#define PACKED __attribute__((__packed__))
#define ALIGN8 __attribute__((aligned(8)))

#define ENABLE_XDPCAP
#include "xdpcap.h"

// We use clang built-in memcpy, but need a function signature to provoke it.
void* memcpy(void*, const void*, unsigned long);

#define DEBUG_LB_MAIN 1

#define ICMPV4_DEST_UNREACHABLE 3
#define ICMPV4_FRAGMENTATION_NEEDED 4
#define ICMPV4_QUOTE_LEN 28
#define ICMPV4_MESSAGE_LEN 36
#define ICMPV4_REPLY_FRAME_LEN 70

#define ICMPV6_PACKET_TOO_BIG 2
#define ICMPV6_QUOTE_LEN 1232
#define ICMPV6_MESSAGE_LEN 1240
#define ICMPV6_REPLY_FRAME_LEN 1294

// clang-format off
struct stat_counters { /* go:Add,String */
  uint64_t rx_packet_total; // HELP Number of packets received against known VIPs.
  uint64_t rx_total_size; // HELP Total size of packets received against known VIPs.

  uint64_t too_short_packet_total; // HELP Number of packets too short to inspect or forward.
  uint64_t unsupported_network_packet_total; // HELP Number of packets passed because they were neither IPv4 nor IPv6.
  uint64_t ipv4_packet_total; // HELP Number of accepted IPv4 packets.
  uint64_t ipv6_packet_total; // HELP Number of accepted IPv6 packets.
  uint64_t ip_option_packet_total; // HELP Number of VIP packets dropped because they had IPv4 options.
  uint64_t non_supported_proto_packet_total; // HELP Number of VIP packets dropped because TCP did not immediately follow the IP header.
  uint64_t no_vip_match_total; // HELP Number of packets passed because their destination did not match a VIP.
  uint64_t mtu_exceeded_packet_total; // HELP Number of packets too large for IPv6 encapsulation over the configured underlay MTU.
  uint64_t icmpv4_frag_needed_total; // HELP Number of ICMPv4 Fragmentation Needed responses sent.
  uint64_t icmpv6_packet_too_big_total; // HELP Number of ICMPv6 Packet Too Big responses sent.
  uint64_t failed_adjust_head_total; // HELP Number of xdp_adjust_head failures.
  uint64_t failed_adjust_tail_total; // HELP Number of xdp_adjust_tail failures.
} ALIGN8;
// clang-format on

struct {
  __uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
  __uint(max_entries, 1);
  __type(key, uint32_t);
  __type(value, struct stat_counters);
} stat_counters_map SEC(".maps");

// lb_config contains both public VIPs. A packet is handled only when its
// destination matches the VIP for its IP family.
struct lb_config { /* go: */
  uint32_t vip4_address;
  uint8_t vip6_address[16];
  uint32_t num_dests;
  uint32_t inner_mtu;
} PACKED;

struct {
  __uint(type, BPF_MAP_TYPE_ARRAY);
  __uint(max_entries, 1);
  __type(key, uint32_t);
  __type(value, struct lb_config);
} lb_config_map SEC(".maps");

// destination_entry carries the outer tunnel addresses and Ethernet address
// needed to send an encapsulated packet to a cache node.
struct destination_entry {
  uint8_t ip6_address[16];
  uint8_t mac_address[ETH_ALEN];
} PACKED;

#define DESTINATIONS_SIZE 255 /* go: */

// destinations_map[0] is the LB itself. Entries from index 1 are cache nodes.
struct {
  __uint(type, BPF_MAP_TYPE_ARRAY);
  __uint(max_entries, (DESTINATIONS_SIZE + 1));
  __type(key, uint32_t);
  __type(value, struct destination_entry);
} destinations_map SEC(".maps");

#if DEBUG_LB_MAIN
#define debugk(fmt, ...) bpf_printk(fmt, ##__VA_ARGS__)
#else
#define debugk(fmt, ...) \
  do {                   \
  } while (0)
#endif

struct icmpv4_frag_needed {
  uint8_t type;
  uint8_t code;
  uint16_t checksum;
  uint16_t unused;
  uint16_t mtu;
} PACKED;

struct icmpv6_packet_too_big {
  uint8_t type;
  uint8_t code;
  uint16_t checksum;
  uint32_t mtu;
} PACKED;

static __always_inline uint16_t fold_checksum(uint32_t sum) {
  sum = (sum & 0xffff) + (sum >> 16);
  sum = (sum & 0xffff) + (sum >> 16);
  return ~sum;
}

// Turn the received IPv4 packet into an ICMP Destination Unreachable /
// Fragmentation Needed response. Adding 28 bytes before the original Ethernet
// frame leaves the original IP header immediately after the new Ethernet,
// IPv4 and ICMP headers, so it can be quoted without copying the whole packet.
static __always_inline int send_icmpv4_frag_needed(
    struct xdp_md* ctx, struct stat_counters* c, uint32_t inner_mtu) {
  void* data = (void*)(uint64_t)ctx->data;
  void* data_end = (void*)(uint64_t)ctx->data_end;
  struct ethhdr* old_eth = data;
  uint8_t old_src_mac[ETH_ALEN];
  uint8_t old_dst_mac[ETH_ALEN];

  if ((void*)(old_eth + 1) > data_end) {
    return XDP_DROP;
  }
  memcpy(old_src_mac, old_eth->h_source, ETH_ALEN);
  memcpy(old_dst_mac, old_eth->h_dest, ETH_ALEN);

  if (bpf_xdp_adjust_head(ctx,
                          -(int)(sizeof(struct iphdr) +
                                 sizeof(struct icmpv4_frag_needed)))) {
    ++c->failed_adjust_head_total;
    return XDP_DROP;
  }

  data = (void*)(uint64_t)ctx->data;
  data_end = (void*)(uint64_t)ctx->data_end;
  if (data_end - data < ICMPV4_REPLY_FRAME_LEN) {
    return XDP_DROP;
  }
  if (bpf_xdp_adjust_tail(ctx,
                          -(int)((data_end - data) -
                                 ICMPV4_REPLY_FRAME_LEN))) {
    ++c->failed_adjust_tail_total;
    return XDP_DROP;
  }

  data = (void*)(uint64_t)ctx->data;
  data_end = (void*)(uint64_t)ctx->data_end;
  if (data + ICMPV4_REPLY_FRAME_LEN > data_end) {
    return XDP_DROP;
  }

  struct ethhdr* eth = data;
  struct iphdr* ip4 = (void*)(eth + 1);
  struct icmpv4_frag_needed* icmp = (void*)(ip4 + 1);
  struct iphdr* quoted_ip4 = (void*)(icmp + 1);

  memcpy(eth->h_source, old_dst_mac, ETH_ALEN);
  memcpy(eth->h_dest, old_src_mac, ETH_ALEN);
  eth->h_proto = htons(ETH_P_IP);

  ip4->version = 4;
  ip4->ihl = 5;
  ip4->tos = 0;
  ip4->tot_len = htons(sizeof(struct iphdr) + ICMPV4_MESSAGE_LEN);
  ip4->id = 0;
  ip4->frag_off = 0;
  ip4->ttl = 64;
  ip4->protocol = IPPROTO_ICMP;
  ip4->check = 0;
  ip4->saddr = quoted_ip4->daddr;
  ip4->daddr = quoted_ip4->saddr;
  uint32_t ip_sum = bpf_csum_diff(0, 0, (uint32_t*)ip4,
                                  sizeof(struct iphdr), 0);
  ip4->check = fold_checksum(ip_sum);

  icmp->type = ICMPV4_DEST_UNREACHABLE;
  icmp->code = ICMPV4_FRAGMENTATION_NEEDED;
  icmp->checksum = 0;
  icmp->unused = 0;
  icmp->mtu = htons((uint16_t)inner_mtu);
  uint32_t icmp_sum = bpf_csum_diff(0, 0, (uint32_t*)icmp,
                                    ICMPV4_MESSAGE_LEN, 0);
  icmp->checksum = fold_checksum(icmp_sum);

  ++c->icmpv4_frag_needed_total;
  return XDP_TX;
}

// ICMPv6 errors quote as much of the invoking packet as possible without
// making the IPv6 response exceed the minimum IPv6 MTU (1280 bytes). An
// oversized inner packet is always large enough to supply the fixed 1232-byte
// quote used here.
static __always_inline int send_icmpv6_packet_too_big(
    struct xdp_md* ctx, struct stat_counters* c, uint32_t inner_mtu) {
  void* data = (void*)(uint64_t)ctx->data;
  void* data_end = (void*)(uint64_t)ctx->data_end;
  struct ethhdr* old_eth = data;
  uint8_t old_src_mac[ETH_ALEN];
  uint8_t old_dst_mac[ETH_ALEN];

  if ((void*)(old_eth + 1) > data_end) {
    return XDP_DROP;
  }
  memcpy(old_src_mac, old_eth->h_source, ETH_ALEN);
  memcpy(old_dst_mac, old_eth->h_dest, ETH_ALEN);

  if (bpf_xdp_adjust_head(ctx,
                          -(int)(sizeof(struct ipv6hdr) +
                                 sizeof(struct icmpv6_packet_too_big)))) {
    ++c->failed_adjust_head_total;
    return XDP_DROP;
  }

  data = (void*)(uint64_t)ctx->data;
  data_end = (void*)(uint64_t)ctx->data_end;
  if (data_end - data < ICMPV6_REPLY_FRAME_LEN) {
    return XDP_DROP;
  }
  if (bpf_xdp_adjust_tail(ctx,
                          -(int)((data_end - data) -
                                 ICMPV6_REPLY_FRAME_LEN))) {
    ++c->failed_adjust_tail_total;
    return XDP_DROP;
  }

  data = (void*)(uint64_t)ctx->data;
  data_end = (void*)(uint64_t)ctx->data_end;
  if (data + ICMPV6_REPLY_FRAME_LEN > data_end) {
    return XDP_DROP;
  }

  struct ethhdr* eth = data;
  struct ipv6hdr* ip6 = (void*)(eth + 1);
  struct icmpv6_packet_too_big* icmp = (void*)(ip6 + 1);
  struct ipv6hdr* quoted_ip6 = (void*)(icmp + 1);

  memcpy(eth->h_source, old_dst_mac, ETH_ALEN);
  memcpy(eth->h_dest, old_src_mac, ETH_ALEN);
  eth->h_proto = htons(ETH_P_IPV6);

  ip6->version = 6;
  ip6->priority = 0;
  ip6->flow_lbl[0] = 0;
  ip6->flow_lbl[1] = 0;
  ip6->flow_lbl[2] = 0;
  ip6->payload_len = htons(ICMPV6_MESSAGE_LEN);
  ip6->nexthdr = IPPROTO_ICMPV6;
  ip6->hop_limit = 64;
  memcpy(&ip6->saddr, &quoted_ip6->daddr, sizeof(ip6->saddr));
  memcpy(&ip6->daddr, &quoted_ip6->saddr, sizeof(ip6->daddr));

  icmp->type = ICMPV6_PACKET_TOO_BIG;
  icmp->code = 0;
  icmp->checksum = 0;
  icmp->mtu = htonl(inner_mtu);

  uint32_t pseudo_len = htonl(ICMPV6_MESSAGE_LEN);
  uint32_t pseudo_next_header = htonl(IPPROTO_ICMPV6);
  uint32_t icmp_sum = bpf_csum_diff(
      0, 0, (uint32_t*)&ip6->saddr,
      sizeof(ip6->saddr) + sizeof(ip6->daddr), 0);
  icmp_sum = bpf_csum_diff(0, 0, &pseudo_len, sizeof(pseudo_len), icmp_sum);
  icmp_sum = bpf_csum_diff(0, 0, &pseudo_next_header,
                           sizeof(pseudo_next_header), icmp_sum);
  // bpf_csum_diff accepts only a limited amount of packet data per call on
  // some kernels. Accumulate the 1240-byte ICMPv6 message in fixed-size,
  // four-byte-aligned chunks.
  icmp_sum = bpf_csum_diff(0, 0, (uint32_t*)((uint8_t*)icmp + 0),
                           256, icmp_sum);
  icmp_sum = bpf_csum_diff(0, 0, (uint32_t*)((uint8_t*)icmp + 256),
                           256, icmp_sum);
  icmp_sum = bpf_csum_diff(0, 0, (uint32_t*)((uint8_t*)icmp + 512),
                           256, icmp_sum);
  icmp_sum = bpf_csum_diff(0, 0, (uint32_t*)((uint8_t*)icmp + 768),
                           256, icmp_sum);
  icmp_sum = bpf_csum_diff(0, 0, (uint32_t*)((uint8_t*)icmp + 1024),
                           ICMPV6_MESSAGE_LEN - 1024, icmp_sum);
  icmp->checksum = fold_checksum(icmp_sum);

  ++c->icmpv6_packet_too_big_total;
  return XDP_TX;
}

SEC("xdp")
int lb_main(struct xdp_md* ctx) {
  void* data = (void*)(uint64_t)ctx->data;
  void* data_end = (void*)(uint64_t)ctx->data_end;

  const uint32_t map_key_zero = 0;
  struct stat_counters* c =
      bpf_map_lookup_elem(&stat_counters_map, &map_key_zero);
  if (!c) {
    EXIT(XDP_PASS);
  }

  struct lb_config* config = bpf_map_lookup_elem(&lb_config_map, &map_key_zero);
  if (!config) {
    EXIT(XDP_PASS);
  }

  struct destination_entry* src_entry =
      bpf_map_lookup_elem(&destinations_map, &map_key_zero);
  if (!src_entry) {
    EXIT(XDP_PASS);
  }

  if (data + sizeof(struct ethhdr) > data_end) {
    ++c->too_short_packet_total;
    EXIT(XDP_PASS);
  }

  struct ethhdr* eth = data;
  uint32_t key = 0;
  uint16_t inner_len = 0;
  uint16_t minimum_inner_len = 0;
  int ip_version = 0;

  if (eth->h_proto == htons(ETH_P_IP)) {
    if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) > data_end) {
      ++c->too_short_packet_total;
      EXIT(XDP_PASS);
    }

    struct iphdr* ip4 = (struct iphdr*)(eth + 1);
    if (ip4->version != 4) {
      ++c->unsupported_network_packet_total;
      EXIT(XDP_PASS);
    }
    if (ip4->daddr != config->vip4_address) {
      ++c->no_vip_match_total;
      EXIT(XDP_PASS);
    }
    if (ip4->ihl != 5) {
      ++c->ip_option_packet_total;
      EXIT(XDP_DROP);
    }
    if (ip4->protocol != IPPROTO_TCP) {
      ++c->non_supported_proto_packet_total;
      EXIT(XDP_DROP);
    }
    if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) +
            sizeof(struct tcphdr) >
        data_end) {
      ++c->too_short_packet_total;
      EXIT(XDP_DROP);
    }

    struct tcphdr* tcp = (struct tcphdr*)(ip4 + 1);
    key = ip4->saddr + tcp->source;
    inner_len = ntohs(ip4->tot_len);
    minimum_inner_len = sizeof(struct iphdr) + sizeof(struct tcphdr);
    ip_version = 4;
    ++c->ipv4_packet_total;
    debugk("incoming IPv4 packet: ip=%pI4 port=%u", &ip4->saddr,
           ntohs(tcp->source));
  } else if (eth->h_proto == htons(ETH_P_IPV6)) {
    if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) > data_end) {
      ++c->too_short_packet_total;
      EXIT(XDP_PASS);
    }

    struct ipv6hdr* ip6 = (struct ipv6hdr*)(eth + 1);
    if (ip6->version != 6) {
      ++c->unsupported_network_packet_total;
      EXIT(XDP_PASS);
    }
    uint32_t* vip6 = (uint32_t*)config->vip6_address;
    if (ip6->daddr.s6_addr32[0] != vip6[0] ||
        ip6->daddr.s6_addr32[1] != vip6[1] ||
        ip6->daddr.s6_addr32[2] != vip6[2] ||
        ip6->daddr.s6_addr32[3] != vip6[3]) {
      ++c->no_vip_match_total;
      EXIT(XDP_PASS);
    }
    // Extension headers are deliberately not handled in the first IPv6
    // implementation. TCP must immediately follow the fixed IPv6 header.
    if (ip6->nexthdr != IPPROTO_TCP) {
      ++c->non_supported_proto_packet_total;
      EXIT(XDP_DROP);
    }
    if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) +
            sizeof(struct tcphdr) >
        data_end) {
      ++c->too_short_packet_total;
      EXIT(XDP_DROP);
    }

    struct tcphdr* tcp = (struct tcphdr*)(ip6 + 1);
    key = ip6->saddr.s6_addr32[0] ^ ip6->saddr.s6_addr32[1] ^
          ip6->saddr.s6_addr32[2] ^ ip6->saddr.s6_addr32[3] ^ tcp->source;
    inner_len = sizeof(struct ipv6hdr) + ntohs(ip6->payload_len);
    minimum_inner_len = sizeof(struct ipv6hdr) + sizeof(struct tcphdr);
    ip_version = 6;
    ++c->ipv6_packet_total;
    debugk("incoming IPv6 packet: port=%u", ntohs(tcp->source));
  } else {
    ++c->unsupported_network_packet_total;
    EXIT(XDP_PASS);
  }

  if (inner_len < minimum_inner_len ||
      data + sizeof(struct ethhdr) + inner_len > data_end) {
    ++c->too_short_packet_total;
    EXIT(XDP_DROP);
  }

  ++c->rx_packet_total;
  c->rx_total_size += data_end - data;

  if (inner_len > config->inner_mtu) {
    ++c->mtu_exceeded_packet_total;
    if (ip_version == 4) {
      EXIT(send_icmpv4_frag_needed(ctx, c, config->inner_mtu));
    }
    EXIT(send_icmpv6_packet_too_big(ctx, c, config->inner_mtu));
  }

  uint32_t dest_idx = (key % config->num_dests) + 1;
  struct destination_entry* dest =
      bpf_map_lookup_elem(&destinations_map, &dest_idx);
  if (!dest) {
    bpf_printk("ASSERTION FAILURE: no dest entry for %d", dest_idx);
    EXIT(XDP_DROP);
  }

  ssize_t padding = (data_end - data) - (sizeof(struct ethhdr) + inner_len);

  // The PoP underlay is IPv6 for both client address families. The original
  // IPv4 or IPv6 packet remains unchanged as the inner packet.
  if (bpf_xdp_adjust_head(ctx, -(int)sizeof(struct ipv6hdr))) {
    ++c->failed_adjust_head_total;
    EXIT(XDP_DROP);
  }

  if (ctx->data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) >
      ctx->data_end) {
    EXIT(XDP_DROP);
  }

  eth = (void*)(uint64_t)ctx->data;
  eth->h_proto = htons(ETH_P_IPV6);
  memcpy(eth->h_source, src_entry->mac_address,
         sizeof(src_entry->mac_address));
  memcpy(eth->h_dest, dest->mac_address, sizeof(dest->mac_address));

  struct ipv6hdr* ip6_outer = (void*)(eth + 1);
  ip6_outer->version = 6;
  ip6_outer->priority = 0;
  ip6_outer->flow_lbl[0] = 0;
  ip6_outer->flow_lbl[1] = 0;
  ip6_outer->flow_lbl[2] = 0;
  ip6_outer->payload_len = htons(inner_len);
  ip6_outer->nexthdr =
      ip_version == 4 ? IPPROTO_IPIP : IPPROTO_IPV6;
  ip6_outer->hop_limit = 64;
  memcpy(&ip6_outer->saddr, src_entry->ip6_address, 16);
  memcpy(&ip6_outer->daddr, dest->ip6_address, 16);

  if (padding > 0) {
    if (bpf_xdp_adjust_tail(ctx, -padding)) {
      ++c->failed_adjust_tail_total;
      EXIT(XDP_DROP);
    }
  }

  EXIT(XDP_TX);
}

#undef debugk

char _license[] SEC("license") = "Dual BSD/GPL";
