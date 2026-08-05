#!/bin/bash
# Redirects intentionally target the invoking user's temporary directory.
# shellcheck disable=SC2024
set -euo pipefail

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
DURATION=${DURATION:-10}
PARALLEL=${PARALLEL:-4}
PORT=${PORT:-5201}
PACKET_RATE=${PACKET_RATE:-1000000}

LB_PID=""
LB_PROCESS_PID=""
LB_BINARY_BYTES=0
BPF_PROGRAM_MEMLOCK_BYTES=0
BPF_MAPS_MEMLOCK_BYTES=0
BPF_JITED_BYTES=0
TMP_DIR=$(mktemp -d)

function cleanup() {
    set +e
    for ns in C0 C1; do
        sudo ip netns exec "${ns}" pkill -x iperf >/dev/null 2>&1
    done
    sudo ip netns exec LB pkill -TERM -x l4lb >/dev/null 2>&1
    if [ -n "${LB_PID}" ]; then
        kill "${LB_PID}" >/dev/null 2>&1
        wait "${LB_PID}" >/dev/null 2>&1
    fi
    rm -rf "${TMP_DIR}"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

function require_positive_integer() {
    local name=$1
    local value=$2

    if ! [[ "${value}" =~ ^[1-9][0-9]*$ ]]; then
        echo "${name} must be a positive integer: ${value}" >&2
        exit 2
    fi
}

function require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "Required command not found: $1" >&2
        exit 1
    fi
}

function stop_servers() {
    for ns in C0 C1; do
        sudo ip netns exec "${ns}" pkill -x iperf >/dev/null 2>&1 || true
    done
}

function read_lb_stat() {
    sudo ip netns exec LB cat "/sys/class/net/net0/statistics/$1"
}

function read_cpu_stat() {
    awk '/^cpu / {
        total = 0
        for (i = 2; i <= NF; i++) {
            total += $i
        }
        idle = $5 + $6
        print total, idle
        exit
    }' /proc/stat
}

function read_l4lb_memory_kib() {
    local field=$1

    awk -v field="${field}:" '$1 == field { print $2; exit }' \
        "/proc/${LB_PROCESS_PID}/status"
}

function read_static_resource_usage() {
    local xdp_program_id program_info map_id map_info map_memlock

    LB_PROCESS_PID=$(sudo ip netns pids LB | while read -r pid; do
        if [ "$(cat "/proc/${pid}/comm")" = "l4lb" ]; then
            echo "${pid}"
            break
        fi
    done)
    if [ -z "${LB_PROCESS_PID}" ]; then
        echo "Could not find the l4lb process" >&2
        exit 1
    fi

    LB_BINARY_BYTES=$(stat -c %s /tmp/ncdn-bin/l4lb)
    xdp_program_id=$(sudo ip netns exec LB ip -details link show net0 | \
        awk '/prog\/xdp/ {
            for (i = 1; i <= NF; i++) {
                if ($i == "id") {
                    print $(i + 1)
                    exit
                }
            }
        }')
    program_info=$(sudo bpftool -j prog show id "${xdp_program_id}")
    BPF_PROGRAM_MEMLOCK_BYTES=$(jq -r '.bytes_memlock // 0' <<<"${program_info}")
    BPF_JITED_BYTES=$(jq -r '.bytes_jited // 0' <<<"${program_info}")

    BPF_MAPS_MEMLOCK_BYTES=0
    while read -r map_id; do
        map_info=$(sudo bpftool -j map show id "${map_id}")
        map_memlock=$(jq -r '.bytes_memlock // 0' <<<"${map_info}")
        BPF_MAPS_MEMLOCK_BYTES=$((BPF_MAPS_MEMLOCK_BYTES + map_memlock))
    done < <(jq -r '.map_ids[]?' <<<"${program_info}")
}

function start_servers() {
    local family=$1
    local vip=$2
    local family_args=()

    if [ "${family}" = "ipv6" ]; then
        family_args=(-V)
    fi

    stop_servers
    for ns in C0 C1; do
        sudo ip netns exec "${ns}" \
            iperf "${family_args[@]}" -s -B "${vip}" -p "${PORT}" -D \
            >/dev/null 2>&1
    done
    sleep 0.2
}

function run_client() {
    local family=$1
    local vip=$2
    local duration=$3
    local output=$4
    local error_output="${output}.stderr"
    local args=(-c "${vip}" -p "${PORT}" -t "${duration}" -P "${PARALLEL}" -y C --ignore-shutdown)

    if [ "${family}" = "ipv6" ]; then
        args=(-V "${args[@]}")
    fi
    if ! sudo ip netns exec U iperf "${args[@]}" >"${output}" 2>"${error_output}"; then
        cat "${error_output}" >&2
        return 1
    fi
}

function run_packet_generator() {
    local family=$1
    local vip=$2
    local duration=$3
    local per_process_rate=$(((PACKET_RATE + PARALLEL - 1) / PARALLEL))
    local packet_count=$((per_process_rate * duration * 2))
    local status
    local family_args=()
    local pids=()

    if [ "${family}" = "ipv6" ]; then
        family_args=(-6)
    fi

    for index in $(seq 1 "${PARALLEL}"); do
        sudo ip netns exec U timeout --signal=INT "${duration}" \
            nping "${family_args[@]}" --tcp --flags syn \
            -g "$((40000 + index))" -p "${PORT}" \
            --rate "${per_process_rate}" -c "${packet_count}" \
            --no-capture --quiet "${vip}" \
            >"${TMP_DIR}/${family}-nping-${index}.log" 2>&1 &
        pids+=("$!")
    done

    for index in "${!pids[@]}"; do
        if wait "${pids[$index]}"; then
            continue
        else
            status=$?
        fi
        if [ "${status}" -ne 124 ] && [ "${status}" -ne 130 ]; then
            cat "${TMP_DIR}/${family}-nping-$((index + 1)).log" >&2
            return "${status}"
        fi
    done
}

function run_case() {
    local family=$1
    local vip=$2
    local profile=$3
    local output="${TMP_DIR}/${family}-${profile}.csv"
    local packets_before bytes_before drops_before packets_after bytes_after drops_after
    local time_before time_after elapsed_ns
    local cpu_total_before cpu_idle_before cpu_total_after cpu_idle_after
    local cpu_total_delta cpu_idle_delta
    local iperf_bits_per_second
    local l4lb_rss_kib l4lb_peak_rss_kib

    echo "Running ${family} ${profile} benchmark..." >&2
    if [ "${profile}" = "throughput" ]; then
        start_servers "${family}" "${vip}"
    else
        stop_servers
    fi

    packets_before=$(read_lb_stat rx_packets)
    bytes_before=$(read_lb_stat rx_bytes)
    drops_before=$(read_lb_stat rx_dropped)
    read -r cpu_total_before cpu_idle_before < <(read_cpu_stat)
    time_before=$(date +%s%N)

    if [ "${profile}" = "throughput" ]; then
        run_client "${family}" "${vip}" "${DURATION}" "${output}"
    else
        run_packet_generator "${family}" "${vip}" "${DURATION}"
    fi

    time_after=$(date +%s%N)
    read -r cpu_total_after cpu_idle_after < <(read_cpu_stat)
    packets_after=$(read_lb_stat rx_packets)
    bytes_after=$(read_lb_stat rx_bytes)
    drops_after=$(read_lb_stat rx_dropped)
    stop_servers

    l4lb_rss_kib=$(read_l4lb_memory_kib VmRSS)
    l4lb_peak_rss_kib=$(read_l4lb_memory_kib VmHWM)

    elapsed_ns=$((time_after - time_before))
    cpu_total_delta=$((cpu_total_after - cpu_total_before))
    cpu_idle_delta=$((cpu_idle_after - cpu_idle_before))
    iperf_bits_per_second=0
    if [ "${profile}" = "throughput" ]; then
        iperf_bits_per_second=$(awk -F, 'NF >= 9 { value = $9 } END { print value + 0 }' "${output}")
        if [ "${iperf_bits_per_second}" -le 0 ]; then
            echo "iperf reported no traffic for ${family} ${profile}" >&2
            cat "${output}.stderr" >&2
            exit 1
        fi
    fi

    awk -v family="${family}" \
        -v profile="${profile}" \
        -v parallel="${PARALLEL}" \
        -v elapsed_ns="${elapsed_ns}" \
        -v packets="$((packets_after - packets_before))" \
        -v bytes="$((bytes_after - bytes_before))" \
        -v drops="$((drops_after - drops_before))" \
        -v iperf_bps="${iperf_bits_per_second}" \
        -v cpu_total_delta="${cpu_total_delta}" \
        -v cpu_idle_delta="${cpu_idle_delta}" \
        -v l4lb_rss_kib="${l4lb_rss_kib}" \
        -v l4lb_peak_rss_kib="${l4lb_peak_rss_kib}" \
        -v l4lb_binary_bytes="${LB_BINARY_BYTES}" \
        -v bpf_program_memlock_bytes="${BPF_PROGRAM_MEMLOCK_BYTES}" \
        -v bpf_maps_memlock_bytes="${BPF_MAPS_MEMLOCK_BYTES}" \
        -v bpf_jited_bytes="${BPF_JITED_BYTES}" \
        'BEGIN {
            seconds = elapsed_ns / 1000000000
            pps = packets / seconds
            ingress_gbps = bytes * 8 / seconds / 1000000000
            tcp_throughput_gbps = ""
            if (profile == "throughput") {
                tcp_throughput_gbps = sprintf("%.6f", iperf_bps / 1000000000)
            }
            cpu = 0
            if (cpu_total_delta > 0) {
                cpu = (cpu_total_delta - cpu_idle_delta) * 100 / cpu_total_delta
            }
            printf "%s,%s,%.3f,%d,%d,%.0f,%.6f,%s,%d,%.2f,%d,%d,%d,%d,%d,%d\n", \
                family, profile, seconds, parallel, packets, pps, \
                ingress_gbps, tcp_throughput_gbps, drops, cpu, \
                l4lb_rss_kib, l4lb_peak_rss_kib, l4lb_binary_bytes, \
                bpf_program_memlock_bytes, bpf_maps_memlock_bytes, bpf_jited_bytes
        }'
}

require_positive_integer DURATION "${DURATION}"
require_positive_integer PARALLEL "${PARALLEL}"
require_positive_integer PORT "${PORT}"
require_positive_integer PACKET_RATE "${PACKET_RATE}"

for command in awk bpftool date ip iperf jq nping pkill stat sudo timeout; do
    require_command "${command}"
done
if ! sudo -n true; then
    echo "Passwordless sudo is required. Run this script inside the devcontainer." >&2
    exit 1
fi

echo "Setting up the network namespace topology..." >&2
if ! sudo "${SCRIPT_DIR}/netns_setup.sh" >"${TMP_DIR}/netns-setup.log" 2>&1; then
    cat "${TMP_DIR}/netns-setup.log" >&2
    exit 1
fi

echo "Starting the L4LB..." >&2
"${SCRIPT_DIR}/run-lb.sh" >"${TMP_DIR}/l4lb.log" 2>&1 &
LB_PID=$!

for _ in $(seq 1 100); do
    if sudo ip netns exec LB ip -details link show net0 2>/dev/null | grep -q 'prog/xdp'; then
        break
    fi
    if ! kill -0 "${LB_PID}" 2>/dev/null; then
        cat "${TMP_DIR}/l4lb.log" >&2
        exit 1
    fi
    sleep 0.1
done
if ! sudo ip netns exec LB ip -details link show net0 | grep -q 'prog/xdp'; then
    echo "Timed out waiting for the L4LB XDP program" >&2
    cat "${TMP_DIR}/l4lb.log" >&2
    exit 1
fi

# Populate the client-router and router-LB neighbor tables without sending
# benchmark traffic through the VIP.
sudo ip netns exec U ping -q -c 1 198.51.100.1 >/dev/null
sudo ip netns exec U ping -q -6 -c 1 2001:db8:0:2::1 >/dev/null
sudo ip netns exec R ping -q -c 1 192.168.88.20 >/dev/null
sudo ip netns exec R ping -q -6 -c 1 2001:db8:0:1::20 >/dev/null
read_static_resource_usage

echo "family,profile,duration_seconds,parallel,ingress_packets,ingress_pps,ingress_gbps,tcp_throughput_gbps,rx_dropped,host_cpu_busy_percent,l4lb_rss_kib,l4lb_peak_rss_kib,l4lb_binary_bytes,bpf_program_memlock_bytes,bpf_maps_memlock_bytes,bpf_jited_bytes"
for family in ipv4 ipv6; do
    if [ "${family}" = "ipv4" ]; then
        vip=192.0.2.10
    else
        vip=2001:db8:100::10
    fi
    run_case "${family}" "${vip}" throughput
    run_case "${family}" "${vip}" packet-rate
done
