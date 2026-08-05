#!/bin/bash
set -euo pipefail

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
source "${SCRIPT_DIR}/mtu-config.sh"

for ns in U R LB C0 C1 O; do
    if sudo ip netns exec "${ns}" ip -o -4 address show scope global | grep -q .; then
        echo "${ns}: an IPv4 address is still configured" >&2
        exit 1
    fi
done

for cache in C0 C1; do
    sudo ip -n "${cache}" -6 route show default | grep -qw "advmss ${IPV6_TCP_MSS}"
    sudo ip -n "${cache}" link show v6tun0 | grep -qw "mtu ${INNER_MTU}"
    sudo ip -n "${cache}" -6 tunnel show v6tun0 | grep -qw 'encaplimit none'
done

sudo ip -n LB -6 route show table local | grep -qw 'local 2001:db8:100::10 dev lo'

cache_key=$(date +%s%N)
headers=$(sudo ip netns exec U curl \
    --noproxy "*" \
    --globoff \
    --ipv6 \
    --silent \
    --show-error \
    --dump-header - \
    --output /dev/null \
    "http://[2001:db8:100::10]:8889/json?verify=${cache_key}" \
    --output /dev/null \
    "http://[2001:db8:100::10]:8889/json?verify=${cache_key}")

grep -qi '^X-Ncdn-Cache: MISS' <<<"${headers}"
grep -qi '^X-Ncdn-Cache: HIT' <<<"${headers}"

echo "IPv6-only: L4LB -> PoP -> Origin, MISS -> HIT: OK"
