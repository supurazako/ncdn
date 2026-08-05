#!/bin/bash

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
cd "${SCRIPT_DIR}"
source "${SCRIPT_DIR}/mtu-config.sh"

IPV6_ONLY=0

if [ "${1:-}" = "--ipv6-only" ]; then
    IPV6_ONLY=1
elif [ -n "${1:-}" ]; then
    echo "Usage: $0 [--ipv6-only]"
    exit 2
fi

if [ "$EUID" -ne 0 ]; then
    echo "Please run as root"
    exit
fi

function add_ns_bare() {
    ip netns del $1
    rm /var/run/netns/$1

    ip netns add $1
    ip -n $1 l set lo up

    # Enable IPv6 and skip Duplicate Address Detection in this disposable
    # topology so addresses are ready as soon as the script finishes.
    ip netns exec $1 sysctl -w net.ipv6.conf.all.disable_ipv6=0
    ip netns exec $1 sysctl -w net.ipv6.conf.default.disable_ipv6=0
    ip netns exec $1 sysctl -w net.ipv6.conf.all.accept_dad=0
    ip netns exec $1 sysctl -w net.ipv6.conf.default.accept_dad=0
    if [ "${IPV6_ONLY}" -eq 0 ]; then
        # disable rp_filter for direct server return (DSR)
        ip netns exec $1 sysctl -w net.ipv4.conf.all.rp_filter=0
        ip netns exec $1 sysctl -w net.ipv4.conf.default.rp_filter=0
    fi
}

function add_ns() {
    ip l del $1-net0

    ip netns del $1
    rm /var/run/netns/$1

    ip netns add $1
    ip -n $1 l set lo up

    ip l add net0 netns $1 type veth peer name $1-net0
    ip -n $1 l set net0 up
    ip l set $1-net0 master brDev
    ip l set $1-net0 up

    # disable TCO - while veth optimizes the TCP transports by
    # skipping checksum computation/verification altogether,
    # we actually need a good checksum since we're going to
    # encap the packets in IP tunnels. Otherwise the tunnel
    # receiver would drop the packets given all its bad csums.
    ip netns exec $1 ethtool --offload net0 tx off rx off

    # Enable IPv6 and skip Duplicate Address Detection in this disposable
    # topology so addresses are ready as soon as the script finishes.
    ip netns exec $1 sysctl -w net.ipv6.conf.all.disable_ipv6=0
    ip netns exec $1 sysctl -w net.ipv6.conf.default.disable_ipv6=0
    ip netns exec $1 sysctl -w net.ipv6.conf.all.accept_dad=0
    ip netns exec $1 sysctl -w net.ipv6.conf.default.accept_dad=0
    if [ "${IPV6_ONLY}" -eq 0 ]; then
        # disable rp_filter for direct server return (DSR)
        ip netns exec $1 sysctl -w net.ipv4.conf.all.rp_filter=0
        ip netns exec $1 sysctl -w net.ipv4.conf.default.rp_filter=0
        ip netns exec $1 sysctl -w net.ipv4.conf.net0.rp_filter=0

        ip -n $1 a add $2 dev net0
    fi
    ip -n $1 -6 a add $3 dev net0 nodad
}

set -x
ip l add name brDev type bridge
ip l set dev brDev up

add_ns_bare U # "198.51.100.200/24"
add_ns R "192.168.88.1/24" "2001:db8:0:1::1/64"
add_ns LB "192.168.88.20/24" "2001:db8:0:1::20/64"
add_ns C0 "192.168.88.10/24" "2001:db8:0:1::10/64"
add_ns C1 "192.168.88.11/24" "2001:db8:0:1::11/64"
add_ns O "192.168.88.30/24" "2001:db8:0:1::30/64"

# Keep both VIPs local to the LB as a routing safety net. Packets handled by
# XDP never reach these addresses, but an accidental XDP_PASS terminates on
# the LB instead of following the default route back to R.
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip -n LB a add 192.0.2.10/32 dev lo
fi
ip -n LB -6 a add 2001:db8:100::10/128 dev lo nodad

# veth: U (198.51.100.200/24) <-> R (198.51.100.1/24)
ip l a net0 netns U type veth peer name netU netns R
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip -n U a add 198.51.100.200/24 dev net0
fi
ip -n U -6 a add 2001:db8:0:2::200/64 dev net0 nodad
ip -n U l set net0 up
ip netns exec U ethtool --offload net0 tx off rx off
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip -n R a add 198.51.100.1/24 dev netU
fi
ip -n R -6 a add 2001:db8:0:2::1/64 dev netU nodad
ip -n R l set netU up
ip netns exec R ethtool --offload netU tx off rx off

# enable routing on R and LB
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip netns exec R sysctl -w net.ipv4.ip_forward=1
    ip netns exec LB sysctl -w net.ipv4.ip_forward=1
    ip netns exec C0 sysctl -w net.ipv4.ip_forward=1
    ip netns exec C1 sysctl -w net.ipv4.ip_forward=1
fi
ip netns exec R sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec LB sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec C0 sysctl -w net.ipv6.conf.all.forwarding=1
ip netns exec C1 sysctl -w net.ipv6.conf.all.forwarding=1

# default route to R
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip -n U r add default via 198.51.100.1
    ip -n LB r add default via 192.168.88.1
    # C0/C1 advertise an MSS that keeps client-to-PoP IPv4 packets within the
    # configured inner MTU after the L4LB adds the outer IPv6 header.
    ip -n C0 r add default via 192.168.88.1 advmss ${IPV4_TCP_MSS}
    ip -n C1 r add default via 192.168.88.1 advmss ${IPV4_TCP_MSS}
fi
ip -n U -6 r add default via 2001:db8:0:2::1
ip -n LB -6 r add default via 2001:db8:0:1::1
# IPv6 has a 40-byte inner IP header, so its advertised MSS is 20 bytes
# smaller than IPv4's.
ip -n C0 -6 r add default via 2001:db8:0:1::1 advmss ${IPV6_TCP_MSS}
ip -n C1 -6 r add default via 2001:db8:0:1::1 advmss ${IPV6_TCP_MSS}
# ip netns exec O ip r add default via 192.168.88.1

# LB->C0 IPv6 underlay tunnel. "mode any" accepts both IPv4-in-IPv6 and
# IPv6-in-IPv6 packets from the L4LB.
ip -n C0 -6 tunnel add v6tun0 mode any remote 2001:db8:0:1::20 local 2001:db8:0:1::10 dev net0 encaplimit none
ip -n C0 l set v6tun0 mtu ${INNER_MTU} up
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip -n C0 a add 192.0.2.10/32 dev v6tun0
fi
ip -n C0 -6 a add 2001:db8:100::10/128 dev v6tun0 nodad

# LB->C1 IPv6 underlay tunnel.
ip -n C1 -6 tunnel add v6tun0 mode any remote 2001:db8:0:1::20 local 2001:db8:0:1::11 dev net0 encaplimit none
ip -n C1 l set v6tun0 mtu ${INNER_MTU} up
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip -n C1 a add 192.0.2.10/32 dev v6tun0
fi
ip -n C1 -6 a add 2001:db8:100::10/128 dev v6tun0 nodad

# Route VIPs to LB
if [ "${IPV6_ONLY}" -eq 0 ]; then
    ip -n R r add 192.0.2.0/24 via 192.168.88.20
fi
ip -n R -6 r add 2001:db8:100::/64 via 2001:db8:0:1::20

# inject dummy xdp prog to workaround https://www.spinics.net/lists/netdev/msg625217.html
(cd c && make dummy.o)
ip l set dev LB-net0 xdp obj c/dummy.o sec xdp
