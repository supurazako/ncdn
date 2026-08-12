#include <assert.h>
#include <linux/bpf.h>
#include <linux/if_ether.h>
#include <linux/ipv6.h>
#include <netinet/in.h>
#include <netinet/ip.h>
#include <netinet/tcp.h>
#include <netinet/udp.h>

#include <bpf/bpf_helpers.h>

#include <stdint.h>
#include <sys/types.h>

#define PACKED __attribute__((__packed__))
#define ALIGN8 __attribute__((aligned(8)))

#define ENABLE_XDPCAP
#include "xdpcap.h"

// We use clang built-in memcpy, but need a function signature to provoke it.
void* memcpy(void*, const void*, unsigned long);

#define DEBUG_LB_MAIN 0

#if L4LB_NO_STATS
#define COUNT(c, field) do { } while (0)
#define COUNT_ADD(c, field, value) do { } while (0)
#else
#define COUNT(c, field) (++(c)->field)
#define COUNT_ADD(c, field, value) ((c)->field += (value))
#endif

#define ICMPV4_DEST_UNREACHABLE 3
#define ICMPV4_FRAGMENTATION_NEEDED 4
#define ICMPV4_TIME_EXCEEDED 11
#define ICMPV4_PARAMETER_PROBLEM 12
#define ICMPV4_QUOTE_LEN 28
#define ICMPV4_MESSAGE_LEN 36
#define ICMPV4_REPLY_FRAME_LEN 70

#define ICMPV6_DEST_UNREACHABLE 1
#define ICMPV6_PACKET_TOO_BIG 2
#define ICMPV6_TIME_EXCEEDED 3
#define ICMPV6_PARAMETER_PROBLEM 4
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
  uint64_t udp_packet_total; // HELP Number of accepted UDP packets for the configured service port.
  uint64_t ip_option_packet_total; // HELP Number of VIP packets dropped because they had IPv4 options.
  uint64_t non_supported_proto_packet_total; // HELP Number of VIP packets dropped because the transport protocol or UDP destination port was unsupported.
  uint64_t no_vip_match_total; // HELP Number of packets passed because their destination did not match a VIP.
  uint64_t no_healthy_destination_total; // HELP Number of VIP packets dropped because no healthy cache destination was available.
  uint64_t mtu_exceeded_packet_total; // HELP Number of packets too large for IPv6 encapsulation over the configured underlay MTU.
  uint64_t icmpv4_frag_needed_total; // HELP Number of ICMPv4 Fragmentation Needed responses sent.
  uint64_t icmpv6_packet_too_big_total; // HELP Number of ICMPv6 Packet Too Big responses sent.
  uint64_t invalid_icmp_error_total; // HELP Number of ICMP errors dropped because their quoted packet could not identify a supported DSR flow.
  uint64_t icmpv4_error_forwarded_total; // HELP Number of ICMPv4 errors forwarded to a cache destination.
  uint64_t icmpv6_error_forwarded_total; // HELP Number of ICMPv6 errors forwarded to a cache destination.
  uint64_t failed_adjust_head_total; // HELP Number of xdp_adjust_head failures.
  uint64_t failed_adjust_tail_total; // HELP Number of xdp_adjust_tail failures.
  uint64_t destination_packet_total[255]; // HELP Number of packets selected for each cache destination.
  uint64_t destination_byte_total[255]; // HELP Number of inner IP bytes selected for each cache destination.
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
  uint8_t src_ip6_address[16];
  uint8_t src_mac_address[6];
  uint16_t udp_dest_port;
  uint32_t num_dests;
  uint32_t inner_mtu;
  uint32_t selection_algorithm;
};

#define SELECTION_ALGORITHM_MODULO 0 /* go: */
#define SELECTION_ALGORITHM_RENDEZVOUS 1 /* go: */
#define SELECTION_ALGORITHM_MAGLEV 2 /* go: */

// A prime-sized table gives every Maglev backend permutation a full cycle.
#define MAGLEV_LOOKUP_SIZE 65537 /* go: */

struct {
  __uint(type, BPF_MAP_TYPE_ARRAY);
  __uint(max_entries, MAGLEV_LOOKUP_SIZE);
  __type(key, uint32_t);
  __type(value, uint32_t);
} selection_lookup_map SEC(".maps");

struct {
  __uint(type, BPF_MAP_TYPE_ARRAY);
  __uint(max_entries, 1);
  __type(key, uint32_t);
  __type(value, struct lb_config);
} lb_config_map SEC(".maps");

#define INLINE_DESTINATIONS_SIZE 2

// inline_lb_config is an experimental two-backend representation. It trades
// backend-count flexibility for one fewer map lookup in the forwarding path.
struct inline_lb_config {
  struct lb_config base;
  uint8_t dest_ip6_addresses[INLINE_DESTINATIONS_SIZE * 16];
  uint8_t dest_mac_addresses[INLINE_DESTINATIONS_SIZE * ETH_ALEN];
};

struct {
  __uint(type, BPF_MAP_TYPE_ARRAY);
  __uint(max_entries, 1);
  __type(key, uint32_t);
  __type(value, struct inline_lb_config);
} inline_lb_config_map SEC(".maps");

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

// Every ICMP error starts with eight bytes before the quoted IP packet.
struct icmp_error_header {
  uint8_t type;
  uint8_t code;
  uint16_t checksum;
  uint32_t rest;
} PACKED;

// ICMP errors are only required to quote the beginning of the transport
// header. The ports are sufficient to recover the original load-balancer key.
struct transport_ports {
  uint16_t source;
  uint16_t dest;
} PACKED;

static __always_inline uint32_t mix32(uint32_t value) {
  value ^= value >> 16;
  value *= 0x85ebca6b;
  value ^= value >> 13;
  value *= 0xc2b2ae35;
  value ^= value >> 16;
  return value;
}

static __always_inline uint32_t flow_hash_ipv4(uint32_t client_address,
                                               uint16_t client_port,
                                               uint16_t service_port) {
  uint32_t ports = ((uint32_t)client_port << 16) | service_port;
  return mix32(client_address ^ ports);
}

static __always_inline uint32_t flow_hash_ipv6(struct in6_addr* client_address,
                                               uint16_t client_port,
                                               uint16_t service_port) {
  uint32_t ports = ((uint32_t)client_port << 16) | service_port;
  uint32_t folded = client_address->s6_addr32[0] ^
                    client_address->s6_addr32[1] ^
                    client_address->s6_addr32[2] ^
                    client_address->s6_addr32[3] ^ ports;
  return mix32(folded);
}

static __always_inline uint32_t select_destination(
    uint32_t flow_hash, struct lb_config* config) {
  uint32_t num_dests = config->num_dests;
  if (num_dests == 0) {
    return 0;
  }

  // Rendezvous and Maglev differ in how the control plane constructs this
  // table. The XDP hot path is a single lookup for both algorithms.
  if (config->selection_algorithm == SELECTION_ALGORITHM_RENDEZVOUS ||
      config->selection_algorithm == SELECTION_ALGORITHM_MAGLEV) {
    uint32_t slot = flow_hash % MAGLEV_LOOKUP_SIZE;
    uint32_t* selected = bpf_map_lookup_elem(&selection_lookup_map, &slot);
    if (!selected || *selected == 0 || *selected > num_dests) {
      return 0;
    }
    return *selected;
  }

#if L4LB_POW2_DESTS
  if ((num_dests & (num_dests - 1)) == 0) {
    return (flow_hash & (num_dests - 1)) + 1;
  }
#endif
  return (flow_hash % num_dests) + 1;
}

#if !L4LB_MINIMAL && !L4LB_L2_DSR
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
    COUNT(c, failed_adjust_head_total);
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
    COUNT(c, failed_adjust_tail_total);
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

  COUNT(c, icmpv4_frag_needed_total);
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
    COUNT(c, failed_adjust_head_total);
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
    COUNT(c, failed_adjust_tail_total);
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

  COUNT(c, icmpv6_packet_too_big_total);
  return XDP_TX;
}
#endif

#if L4LB_MINIMAL
SEC("xdp")
int lb_main(struct xdp_md* ctx) {
  void* data = (void*)(uint64_t)ctx->data;
  void* data_end = (void*)(uint64_t)ctx->data_end;
  const uint32_t map_key_zero = 0;
  struct lb_config* config = bpf_map_lookup_elem(&lb_config_map, &map_key_zero);
  if (!config || data + sizeof(struct ethhdr) > data_end) {
    EXIT(XDP_PASS);
  }

  struct ethhdr* eth = data;
  uint32_t key;
  uint16_t inner_len;
  int ip_version;

  if (eth->h_proto == htons(ETH_P_IP)) {
    if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) > data_end) {
      EXIT(XDP_PASS);
    }
    struct iphdr* ip4 = (void*)(eth + 1);
    if (ip4->version != 4 || ip4->daddr != config->vip4_address) {
      EXIT(XDP_PASS);
    }
    if (ip4->ihl != 5) {
      EXIT(XDP_DROP);
    }
    if (ip4->protocol == IPPROTO_TCP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) +
              sizeof(struct tcphdr) >
          data_end) {
        EXIT(XDP_DROP);
      }
      struct tcphdr* tcp = (void*)(ip4 + 1);
      key = flow_hash_ipv4(ip4->saddr, tcp->source, tcp->dest);
    } else if (ip4->protocol == IPPROTO_UDP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) +
              sizeof(struct udphdr) >
          data_end) {
        EXIT(XDP_DROP);
      }
      struct udphdr* udp = (void*)(ip4 + 1);
      if (config->udp_dest_port == 0 ||
          ntohs(udp->dest) != config->udp_dest_port) {
        EXIT(XDP_DROP);
      }
      key = flow_hash_ipv4(ip4->saddr, udp->source, udp->dest);
    } else {
      EXIT(XDP_DROP);
    }
    inner_len = ntohs(ip4->tot_len);
    ip_version = 4;
  } else if (eth->h_proto == htons(ETH_P_IPV6)) {
    if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) > data_end) {
      EXIT(XDP_PASS);
    }
    struct ipv6hdr* ip6 = (void*)(eth + 1);
    uint32_t* vip6 = (uint32_t*)config->vip6_address;
    if (ip6->version != 6 ||
        ip6->daddr.s6_addr32[0] != vip6[0] ||
        ip6->daddr.s6_addr32[1] != vip6[1] ||
        ip6->daddr.s6_addr32[2] != vip6[2] ||
        ip6->daddr.s6_addr32[3] != vip6[3]) {
      EXIT(XDP_PASS);
    }
    if (ip6->nexthdr == IPPROTO_TCP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) +
              sizeof(struct tcphdr) >
          data_end) {
        EXIT(XDP_DROP);
      }
      struct tcphdr* tcp = (void*)(ip6 + 1);
      key = flow_hash_ipv6(&ip6->saddr, tcp->source, tcp->dest);
    } else if (ip6->nexthdr == IPPROTO_UDP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) +
              sizeof(struct udphdr) >
          data_end) {
        EXIT(XDP_DROP);
      }
      struct udphdr* udp = (void*)(ip6 + 1);
      if (config->udp_dest_port == 0 ||
          ntohs(udp->dest) != config->udp_dest_port) {
        EXIT(XDP_DROP);
      }
      key = flow_hash_ipv6(&ip6->saddr, udp->source, udp->dest);
    } else {
      EXIT(XDP_DROP);
    }
    inner_len = sizeof(struct ipv6hdr) + ntohs(ip6->payload_len);
    ip_version = 6;
  } else {
    EXIT(XDP_PASS);
  }

  if (data + sizeof(struct ethhdr) + inner_len > data_end ||
      inner_len > config->inner_mtu || config->num_dests == 0) {
    EXIT(XDP_DROP);
  }

  uint32_t dest_idx = select_destination(key, config);
  if (dest_idx == 0) {
    EXIT(XDP_DROP);
  }
  struct destination_entry* dest =
      bpf_map_lookup_elem(&destinations_map, &dest_idx);
  if (!dest) {
    EXIT(XDP_DROP);
  }
#if !L4LB_KEEP_PADDING
  ssize_t padding = (data_end - data) - (sizeof(struct ethhdr) + inner_len);
#endif

  if (bpf_xdp_adjust_head(ctx, -(int)sizeof(struct ipv6hdr))) {
    EXIT(XDP_DROP);
  }
  if (ctx->data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) >
      ctx->data_end) {
    EXIT(XDP_DROP);
  }

  eth = (void*)(uint64_t)ctx->data;
  eth->h_proto = htons(ETH_P_IPV6);
  memcpy(eth->h_source, config->src_mac_address, ETH_ALEN);
  memcpy(eth->h_dest, dest->mac_address, ETH_ALEN);

  struct ipv6hdr* outer = (void*)(eth + 1);
  outer->version = 6;
  outer->priority = 0;
  outer->flow_lbl[0] = 0;
  outer->flow_lbl[1] = 0;
  outer->flow_lbl[2] = 0;
  outer->payload_len = htons(inner_len);
  outer->nexthdr = ip_version == 4 ? IPPROTO_IPIP : IPPROTO_IPV6;
  outer->hop_limit = 64;
  memcpy(&outer->saddr, config->src_ip6_address, 16);
  memcpy(&outer->daddr, dest->ip6_address, 16);

#if !L4LB_KEEP_PADDING
  if (padding > 0 && bpf_xdp_adjust_tail(ctx, -padding)) {
    EXIT(XDP_DROP);
  }
#endif
  EXIT(XDP_TX);
}
#else
SEC("xdp")
int lb_main(struct xdp_md* ctx) {
  void* data = (void*)(uint64_t)ctx->data;
  void* data_end = (void*)(uint64_t)ctx->data_end;

  const uint32_t map_key_zero = 0;
#if L4LB_NO_STATS
  struct stat_counters* c = 0;
#else
  struct stat_counters* c =
      bpf_map_lookup_elem(&stat_counters_map, &map_key_zero);
  if (!c) {
    EXIT(XDP_PASS);
  }
#endif

#if L4LB_INLINE_DEST
  struct inline_lb_config* inline_config =
      bpf_map_lookup_elem(&inline_lb_config_map, &map_key_zero);
  if (!inline_config) {
    EXIT(XDP_PASS);
  }
  struct lb_config* config = &inline_config->base;
#else
  struct lb_config* config = bpf_map_lookup_elem(&lb_config_map, &map_key_zero);
#endif
  if (!config) {
    EXIT(XDP_PASS);
  }

  if (data + sizeof(struct ethhdr) > data_end) {
    COUNT(c, too_short_packet_total);
    EXIT(XDP_PASS);
  }

  struct ethhdr* eth = data;
  uint32_t key = 0;
  uint16_t inner_len = 0;
  uint16_t minimum_inner_len = 0;
  int ip_version = 0;
  int icmp_error_version = 0;

  if (eth->h_proto == htons(ETH_P_IP)) {
    if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) > data_end) {
      COUNT(c, too_short_packet_total);
      EXIT(XDP_PASS);
    }

    struct iphdr* ip4 = (struct iphdr*)(eth + 1);
    if (ip4->version != 4) {
      COUNT(c, unsupported_network_packet_total);
      EXIT(XDP_PASS);
    }
    if (ip4->daddr != config->vip4_address) {
      COUNT(c, no_vip_match_total);
      EXIT(XDP_PASS);
    }
    if (ip4->ihl != 5) {
      COUNT(c, ip_option_packet_total);
      EXIT(XDP_DROP);
    }
    if (ip4->protocol == IPPROTO_TCP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) +
              sizeof(struct tcphdr) >
          data_end) {
        COUNT(c, too_short_packet_total);
        EXIT(XDP_DROP);
      }

      struct tcphdr* tcp = (struct tcphdr*)(ip4 + 1);
      key = flow_hash_ipv4(ip4->saddr, tcp->source, tcp->dest);
      minimum_inner_len = sizeof(struct iphdr) + sizeof(struct tcphdr);
      debugk("incoming IPv4 packet: ip=%pI4 port=%u", &ip4->saddr,
             ntohs(tcp->source));
    } else if (ip4->protocol == IPPROTO_UDP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) +
              sizeof(struct udphdr) >
          data_end) {
        COUNT(c, too_short_packet_total);
        EXIT(XDP_DROP);
      }

      struct udphdr* udp = (struct udphdr*)(ip4 + 1);
      if (config->udp_dest_port == 0 ||
          ntohs(udp->dest) != config->udp_dest_port) {
        COUNT(c, non_supported_proto_packet_total);
        EXIT(XDP_DROP);
      }
      key = flow_hash_ipv4(ip4->saddr, udp->source, udp->dest);
      minimum_inner_len = sizeof(struct iphdr) + sizeof(struct udphdr);
      COUNT(c, udp_packet_total);
      debugk("incoming IPv4 UDP packet: ip=%pI4 port=%u", &ip4->saddr,
             ntohs(udp->source));
    } else if (ip4->protocol == IPPROTO_ICMP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct iphdr) +
              sizeof(struct icmp_error_header) + sizeof(struct iphdr) +
              sizeof(struct transport_ports) >
          data_end) {
        COUNT(c, too_short_packet_total);
        EXIT(XDP_DROP);
      }

      struct icmp_error_header* icmp = (void*)(ip4 + 1);
      if (icmp->type != ICMPV4_DEST_UNREACHABLE &&
          icmp->type != ICMPV4_TIME_EXCEEDED &&
          icmp->type != ICMPV4_PARAMETER_PROBLEM) {
        COUNT(c, non_supported_proto_packet_total);
        EXIT(XDP_DROP);
      }

      struct iphdr* quoted_ip4 = (void*)(icmp + 1);
      struct transport_ports* quoted_ports = (void*)(quoted_ip4 + 1);
      if (quoted_ip4->version != 4 || quoted_ip4->ihl != 5 ||
          (quoted_ip4->protocol != IPPROTO_TCP &&
           (quoted_ip4->protocol != IPPROTO_UDP ||
            config->udp_dest_port == 0 ||
            ntohs(quoted_ports->source) != config->udp_dest_port)) ||
          (quoted_ip4->frag_off & htons(0x1fff)) != 0 ||
          quoted_ip4->saddr != config->vip4_address) {
        COUNT(c, invalid_icmp_error_total);
        EXIT(XDP_DROP);
      }
      key = flow_hash_ipv4(quoted_ip4->daddr, quoted_ports->dest,
                           quoted_ports->source);
      minimum_inner_len = sizeof(struct iphdr) +
                          sizeof(struct icmp_error_header) +
                          sizeof(struct iphdr) +
                          sizeof(struct transport_ports);
      icmp_error_version = 4;
      debugk("incoming ICMPv4 error: quoted_ip=%pI4 port=%u",
             &quoted_ip4->daddr, ntohs(quoted_ports->dest));
    } else {
      COUNT(c, non_supported_proto_packet_total);
      EXIT(XDP_DROP);
    }

    inner_len = ntohs(ip4->tot_len);
    ip_version = 4;
    COUNT(c, ipv4_packet_total);
  } else if (eth->h_proto == htons(ETH_P_IPV6)) {
    if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) > data_end) {
      COUNT(c, too_short_packet_total);
      EXIT(XDP_PASS);
    }

    struct ipv6hdr* ip6 = (struct ipv6hdr*)(eth + 1);
    if (ip6->version != 6) {
      COUNT(c, unsupported_network_packet_total);
      EXIT(XDP_PASS);
    }
    uint32_t* vip6 = (uint32_t*)config->vip6_address;
    if (ip6->daddr.s6_addr32[0] != vip6[0] ||
        ip6->daddr.s6_addr32[1] != vip6[1] ||
        ip6->daddr.s6_addr32[2] != vip6[2] ||
        ip6->daddr.s6_addr32[3] != vip6[3]) {
      COUNT(c, no_vip_match_total);
      EXIT(XDP_PASS);
    }
    // Extension headers are deliberately not handled in the first IPv6
    // implementation. TCP or ICMPv6 must immediately follow the fixed header.
    if (ip6->nexthdr == IPPROTO_TCP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) +
              sizeof(struct tcphdr) >
          data_end) {
        COUNT(c, too_short_packet_total);
        EXIT(XDP_DROP);
      }

      struct tcphdr* tcp = (struct tcphdr*)(ip6 + 1);
      key = flow_hash_ipv6(&ip6->saddr, tcp->source, tcp->dest);
      minimum_inner_len = sizeof(struct ipv6hdr) + sizeof(struct tcphdr);
      debugk("incoming IPv6 packet: port=%u", ntohs(tcp->source));
    } else if (ip6->nexthdr == IPPROTO_UDP) {
      if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) +
              sizeof(struct udphdr) >
          data_end) {
        COUNT(c, too_short_packet_total);
        EXIT(XDP_DROP);
      }

      struct udphdr* udp = (struct udphdr*)(ip6 + 1);
      if (config->udp_dest_port == 0 ||
          ntohs(udp->dest) != config->udp_dest_port) {
        COUNT(c, non_supported_proto_packet_total);
        EXIT(XDP_DROP);
      }
      key = flow_hash_ipv6(&ip6->saddr, udp->source, udp->dest);
      minimum_inner_len = sizeof(struct ipv6hdr) + sizeof(struct udphdr);
      COUNT(c, udp_packet_total);
      debugk("incoming IPv6 UDP packet: port=%u", ntohs(udp->source));
    } else if (ip6->nexthdr == IPPROTO_ICMPV6) {
      if (data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) +
              sizeof(struct icmp_error_header) + sizeof(struct ipv6hdr) +
              sizeof(struct transport_ports) >
          data_end) {
        COUNT(c, too_short_packet_total);
        EXIT(XDP_DROP);
      }

      struct icmp_error_header* icmp = (void*)(ip6 + 1);
      if (icmp->type != ICMPV6_DEST_UNREACHABLE &&
          icmp->type != ICMPV6_PACKET_TOO_BIG &&
          icmp->type != ICMPV6_TIME_EXCEEDED &&
          icmp->type != ICMPV6_PARAMETER_PROBLEM) {
        COUNT(c, non_supported_proto_packet_total);
        EXIT(XDP_DROP);
      }

      struct ipv6hdr* quoted_ip6 = (void*)(icmp + 1);
      struct transport_ports* quoted_ports = (void*)(quoted_ip6 + 1);
      uint32_t* vip6_quoted = (uint32_t*)config->vip6_address;
      if (quoted_ip6->version != 6 ||
          (quoted_ip6->nexthdr != IPPROTO_TCP &&
           (quoted_ip6->nexthdr != IPPROTO_UDP ||
            config->udp_dest_port == 0 ||
            ntohs(quoted_ports->source) != config->udp_dest_port)) ||
          quoted_ip6->saddr.s6_addr32[0] != vip6_quoted[0] ||
          quoted_ip6->saddr.s6_addr32[1] != vip6_quoted[1] ||
          quoted_ip6->saddr.s6_addr32[2] != vip6_quoted[2] ||
          quoted_ip6->saddr.s6_addr32[3] != vip6_quoted[3]) {
        COUNT(c, invalid_icmp_error_total);
        EXIT(XDP_DROP);
      }
      key = flow_hash_ipv6(&quoted_ip6->daddr, quoted_ports->dest,
                           quoted_ports->source);
      minimum_inner_len = sizeof(struct ipv6hdr) +
                          sizeof(struct icmp_error_header) +
                          sizeof(struct ipv6hdr) +
                          sizeof(struct transport_ports);
      icmp_error_version = 6;
      debugk("incoming ICMPv6 error: port=%u", ntohs(quoted_ports->dest));
    } else {
      COUNT(c, non_supported_proto_packet_total);
      EXIT(XDP_DROP);
    }

    inner_len = sizeof(struct ipv6hdr) + ntohs(ip6->payload_len);
    ip_version = 6;
    COUNT(c, ipv6_packet_total);
  } else {
    COUNT(c, unsupported_network_packet_total);
    EXIT(XDP_PASS);
  }

  if (inner_len < minimum_inner_len ||
      data + sizeof(struct ethhdr) + inner_len > data_end) {
    COUNT(c, too_short_packet_total);
    EXIT(XDP_DROP);
  }

  COUNT(c, rx_packet_total);
  COUNT_ADD(c, rx_total_size, data_end - data);

#if !L4LB_L2_DSR
  if (inner_len > config->inner_mtu) {
    COUNT(c, mtu_exceeded_packet_total);
    // An ICMP error must never trigger another ICMP error.
    if (icmp_error_version != 0) {
      EXIT(XDP_DROP);
    }
    if (ip_version == 4) {
      EXIT(send_icmpv4_frag_needed(ctx, c, config->inner_mtu));
    }
    EXIT(send_icmpv6_packet_too_big(ctx, c, config->inner_mtu));
  }
#endif

  if (config->num_dests == 0) {
    COUNT(c, no_healthy_destination_total);
    EXIT(XDP_DROP);
  }

  uint32_t dest_idx = select_destination(key, config);
  if (dest_idx == 0) {
    EXIT(XDP_DROP);
  }
#if L4LB_INLINE_DEST
  if (dest_idx > INLINE_DESTINATIONS_SIZE) {
    EXIT(XDP_DROP);
  }
  uint8_t* dest_ip6 =
      &inline_config->dest_ip6_addresses[(dest_idx - 1) * 16];
  uint8_t* dest_mac =
      &inline_config->dest_mac_addresses[(dest_idx - 1) * ETH_ALEN];
#else
  struct destination_entry* dest =
      bpf_map_lookup_elem(&destinations_map, &dest_idx);
  if (!dest) {
    bpf_printk("ASSERTION FAILURE: no dest entry for %d", dest_idx);
    EXIT(XDP_DROP);
  }
  uint8_t* dest_ip6 = dest->ip6_address;
  uint8_t* dest_mac = dest->mac_address;
#endif

  // dest_idx starts at one because destinations_map[0] is the L4LB itself.
  // These fixed arrays reuse the existing per-CPU stats lookup, so observing
  // distribution does not add another map lookup to the forwarding hot path.
  uint32_t stats_idx = dest_idx - 1;
  if (stats_idx < DESTINATIONS_SIZE) {
    COUNT(c, destination_packet_total[stats_idx]);
    COUNT_ADD(c, destination_byte_total[stats_idx], inner_len);
  }

#if L4LB_L2_DSR
  // The cache is directly reachable on the same Ethernet segment. Preserve
  // the original IP packet and select a cache using only the destination MAC.
  memcpy(eth->h_source, config->src_mac_address, ETH_ALEN);
  memcpy(eth->h_dest, dest_mac, ETH_ALEN);
  if (icmp_error_version == 4) {
    COUNT(c, icmpv4_error_forwarded_total);
  } else if (icmp_error_version == 6) {
    COUNT(c, icmpv6_error_forwarded_total);
  }
  EXIT(XDP_TX);
#endif

#if !L4LB_KEEP_PADDING
  ssize_t padding = (data_end - data) - (sizeof(struct ethhdr) + inner_len);
#endif

  // The PoP underlay is IPv6 for both client address families. The original
  // IPv4 or IPv6 packet remains unchanged as the inner packet.
  if (bpf_xdp_adjust_head(ctx, -(int)sizeof(struct ipv6hdr))) {
    COUNT(c, failed_adjust_head_total);
    EXIT(XDP_DROP);
  }

  if (ctx->data + sizeof(struct ethhdr) + sizeof(struct ipv6hdr) >
      ctx->data_end) {
    EXIT(XDP_DROP);
  }

  eth = (void*)(uint64_t)ctx->data;
  eth->h_proto = htons(ETH_P_IPV6);
  memcpy(eth->h_source, config->src_mac_address,
         sizeof(config->src_mac_address));
  memcpy(eth->h_dest, dest_mac, ETH_ALEN);

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
  memcpy(&ip6_outer->saddr, config->src_ip6_address, 16);
  memcpy(&ip6_outer->daddr, dest_ip6, 16);

#if !L4LB_KEEP_PADDING
  if (padding > 0) {
    if (bpf_xdp_adjust_tail(ctx, -padding)) {
      COUNT(c, failed_adjust_tail_total);
      EXIT(XDP_DROP);
    }
  }
#endif

  if (icmp_error_version == 4) {
    COUNT(c, icmpv4_error_forwarded_total);
  } else if (icmp_error_version == 6) {
    COUNT(c, icmpv6_error_forwarded_total);
  }

  EXIT(XDP_TX);
}
#endif

#undef debugk

char _license[] SEC("license") = "Dual BSD/GPL";
