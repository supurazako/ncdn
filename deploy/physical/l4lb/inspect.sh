#!/usr/bin/env bash
set -u

usage() {
  cat <<'EOF'
Usage: ./inspect.sh [interface]

L4LB候補NICと実行環境を読み取り専用で調査する。
interfaceを省略した場合はdefault routeのNICを選ぶ。
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if ! command -v ip >/dev/null 2>&1; then
  echo "error: ip command is required (install iproute2)" >&2
  exit 1
fi

interface=${1:-}
if [[ -z "${interface}" ]]; then
  interface=$(ip -4 route show default 2>/dev/null | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "dev") { print $(i+1); exit } }')
fi
if [[ -z "${interface}" ]]; then
  interface=$(ip -6 route show default 2>/dev/null | awk 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "dev") { print $(i+1); exit } }')
fi
if [[ -z "${interface}" ]]; then
  echo "error: default routeからNICを選べません。引数で指定してください" >&2
  usage >&2
  exit 1
fi
if [[ ! -e "/sys/class/net/${interface}" ]]; then
  echo "error: interface ${interface} does not exist" >&2
  exit 1
fi

section() {
  printf '\n[%s]\n' "$1"
}

read_first_address() {
  local family=$1
  local scope=$2
  ip -o "-${family}" address show dev "${interface}" scope "${scope}" 2>/dev/null \
    | awk 'NR == 1 { split($4, address, "/"); print address[1] }'
}

ipv4=$(read_first_address 4 global)
ipv6=$(read_first_address 6 global)
ipv6_count=$(ip -o -6 address show dev "${interface}" scope global 2>/dev/null | wc -l)
mac=$(cat "/sys/class/net/${interface}/address")
mtu=$(cat "/sys/class/net/${interface}/mtu")
state=$(cat "/sys/class/net/${interface}/operstate")

section system
printf 'hostname: %s\n' "$(hostname)"
printf 'architecture: %s\n' "$(uname -m)"
printf 'kernel: %s\n' "$(uname -r)"

section interfaces
ip -br link
ip -br address

section selected-interface
printf 'interface: %s\n' "${interface}"
printf 'state: %s\n' "${state}"
printf 'mac: %s\n' "${mac}"
printf 'mtu: %s\n' "${mtu}"
printf 'ipv4: %s\n' "${ipv4:-not-configured}"
printf 'ipv6-global: %s\n' "${ipv6:-not-configured}"
ip -details link show dev "${interface}"

section driver
if command -v ethtool >/dev/null 2>&1; then
  ethtool -i "${interface}" 2>&1 || true
else
  echo 'ethtool: not installed'
fi

section routes
ip route
ip -6 route

section neighbors
ip neigh show dev "${interface}"
ip -6 neigh show dev "${interface}"

section xdp-prerequisites
printf 'effective-user: %s (uid=%s)\n' "$(id -un)" "$(id -u)"
printf 'memlock-limit-kbytes: %s\n' "$(ulimit -l)"
if [[ -d /sys/fs/bpf ]]; then
  echo 'bpffs-path: /sys/fs/bpf exists'
else
  echo 'bpffs-path: /sys/fs/bpf is missing'
fi
if command -v bpftool >/dev/null 2>&1; then
  printf 'bpftool: %s\n' "$(command -v bpftool)"
else
  echo 'bpftool: not installed (optional for inspection)'
fi

section deploy-files
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "${script_dir}/../../.." && pwd)
binary_dir="${repo_root}/dist/linux-amd64/l4lb"
for file in l4lb lb-full.o README.md; do
  if [[ -f "${binary_dir}/${file}" ]]; then
    printf 'found: %s\n' "${binary_dir}/${file}"
  else
    printf 'missing: %s\n' "${binary_dir}/${file}"
  fi
done

section configuration-template
cat <<EOF
L4LB_INTERFACE=${interface}
L4LB_IPV4=${ipv4:-PLEASE_SET}
L4LB_IPV6=${ipv6:-PLEASE_SET}
L4LB_MAC=${mac}
UNDERLAY_MTU=${mtu}
VIP4=PLEASE_SET
VIP6=PLEASE_SET
L7LB_IPV6=PLEASE_SET
L7LB_MAC=PLEASE_SET
EOF

section warnings
if [[ "$(uname -m)" != "x86_64" ]]; then
  echo 'warning: linux-amd64 binaryはこのarchitectureでは実行できません'
fi
if [[ -z "${ipv6}" ]]; then
  echo 'warning: L7LBへのencapsulation用global IPv6 addressがありません'
fi
if (( ipv6_count > 1 )); then
  echo 'warning: global IPv6 addressが複数あります。L4LB_IPV6には配布された固定addressを選んでください'
fi
if (( mtu < 1320 )); then
  echo 'warning: L4LBが要求するIPv6 underlay MTU 1320未満です'
fi
if [[ "${state}" != "up" ]]; then
  echo 'warning: 選択したNICがupではありません'
fi

echo
echo 'このscriptはネットワーク設定を変更していません。'
