#!/bin/bash
set -euo pipefail

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
source "${SCRIPT_DIR}/mtu-config.sh"

function verify_cache_path() {
    local family=$1
    local url=$2
    local cache_key
    local headers

    cache_key=$(date +%s%N)
    headers=$(sudo ip netns exec U curl \
        --noproxy "*" \
        --globoff \
        --silent \
        --show-error \
        --dump-header - \
        --output /dev/null \
        "-${family}" \
        "${url}?verify=${cache_key}" \
        --output /dev/null \
        "${url}?verify=${cache_key}")

    if ! grep -qi '^X-Ncdn-Cache: MISS' <<<"${headers}"; then
        echo "IPv${family}: expected a cache MISS" >&2
        return 1
    fi
    if ! grep -qi '^X-Ncdn-Cache: HIT' <<<"${headers}"; then
        echo "IPv${family}: expected a cache HIT" >&2
        return 1
    fi

    if [ "${L4LB_VARIANT:-full}" = "l2-dsr" ]; then
        echo "IPv${family}: L2 DSR -> PoP -> IPv6 Origin, MISS -> HIT: OK"
    elif [ "${family}" = "4" ]; then
        echo "IPv4: IPv4-in-IPv6 -> PoP -> IPv6 Origin, MISS -> HIT: OK"
    else
        echo "IPv6: IPv6-in-IPv6 -> PoP -> IPv6 Origin, MISS -> HIT: OK"
    fi
}

function verify_advertised_mss() {
    local cache

    for cache in C0 C1; do
        if ! sudo ip -n "${cache}" route show default | grep -qw "advmss ${IPV4_TCP_MSS}"; then
            echo "${cache}: expected IPv4 advmss ${IPV4_TCP_MSS}" >&2
            return 1
        fi
        if ! sudo ip -n "${cache}" -6 route show default | grep -qw "advmss ${IPV6_TCP_MSS}"; then
            echo "${cache}: expected IPv6 advmss ${IPV6_TCP_MSS}" >&2
            return 1
        fi
        if ! sudo ip -n "${cache}" link show v6tun0 | grep -qw "mtu ${INNER_MTU}"; then
            echo "${cache}: expected v6tun0 MTU ${INNER_MTU}" >&2
            return 1
        fi
        if ! sudo ip -n "${cache}" -6 tunnel show v6tun0 | grep -qw 'encaplimit none'; then
            echo "${cache}: expected v6tun0 encaplimit none" >&2
            return 1
        fi
    done

    echo "C0/C1: underlay MTU ${UNDERLAY_MTU}, inner MTU ${INNER_MTU}, IPv4 MSS ${IPV4_TCP_MSS}, IPv6 MSS ${IPV6_TCP_MSS}: OK"
}

function verify_lb_owns_vips() {
    if ! sudo ip -n LB route show table local | grep -qw 'local 192.0.2.10 dev lo'; then
        echo "LB: IPv4 VIP is not local to loopback" >&2
        return 1
    fi
    if ! sudo ip -n LB -6 route show table local | grep -qw 'local 2001:db8:100::10 dev lo'; then
        echo "LB: IPv6 VIP is not local to loopback" >&2
        return 1
    fi
    echo "LB: IPv4/IPv6 VIP local routes: OK"
}

verify_cache_path 4 "http://192.0.2.10:8889/json"
verify_cache_path 6 "http://[2001:db8:100::10]:8889/json"
verify_advertised_mss
verify_lb_owns_vips
