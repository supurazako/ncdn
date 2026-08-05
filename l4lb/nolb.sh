#!/bin/bash

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
source "${SCRIPT_DIR}/mtu-config.sh"

# exit if I'm not root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root"
    exit
fi

echo "For bisecting, setup an IPv6 underlay tunnel instead of LB..."
set -x

ip -n LB l set net0 xdp off

ip -n LB -6 tunnel add v6tun0 mode any remote 2001:db8:0:1::10 local 2001:db8:0:1::20 dev net0 encaplimit none
ip -n LB l set v6tun0 mtu ${INNER_MTU} up
ip -n LB r add 192.0.2.0/24 dev v6tun0
ip -n LB -6 r add 2001:db8:100::/64 dev v6tun0
