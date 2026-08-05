#!/bin/bash
set -e

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
source "${SCRIPT_DIR}/mtu-config.sh"

export MY_USER=${USER}
export SRC_DIR=$(readlink -f "${SCRIPT_DIR}/..")
export BIN_DIR=/tmp/ncdn-bin
mkdir -p ${BIN_DIR}

set -x
(cd ${SRC_DIR}/l4lb/c && make)
go build -o ${BIN_DIR}/l4lb ${SRC_DIR}/l4lb/cmd
set +x

cd ${SRC_DIR}/l4lb

dests=""

for ns in LB C0 C1; do
    ip6=$(sudo ip netns exec ${ns} ip -json -f inet6 a show net0 | jq -r '.[]?.addr_info[]? | select(.scope == "global") | .local')
    if [ -z "${ip6}" ]; then
        echo "No IPv6 address found on ${ns}:net0" >&2
        exit 1
    fi
    mac=$(sudo ip netns exec ${ns} cat /sys/class/net/net0/address)

    dests="${dests}${ip6};${mac},"
done

echo ${dests}

sudo ip -n LB -6 tunnel del v6tun0 || echo "no v6tun0. good" # in case it exists from a `nolb.sh` run
sudo ip netns exec LB ${BIN_DIR}/l4lb -xdpcapHookPath="" -dests="${dests}" -vip6="2001:db8:100::10" -underlayMTU="${UNDERLAY_MTU}"
