#!/bin/bash
# Redirects intentionally target the invoking user's temporary directory.
# shellcheck disable=SC2024
set -euo pipefail

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
DURATION=${DURATION:-10}
PARALLEL=${PARALLEL:-4}
PORT=${PORT:-5201}
PACKET_RATES=${PACKET_RATES:-${PACKET_RATE:-"100000 250000 500000 1000000"}}
REPETITIONS=${REPETITIONS:-3}
WARMUP_DURATION=${WARMUP_DURATION:-1}
MAX_FORWARDING_DROP_PERCENT=${MAX_FORWARDING_DROP_PERCENT:-0.1}
MIN_TARGET_ACHIEVEMENT_PERCENT=${MIN_TARGET_ACHIEVEMENT_PERCENT:-95}
MAX_TARGET_ACHIEVEMENT_PERCENT=${MAX_TARGET_ACHIEVEMENT_PERCENT:-105}

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

function require_percentage() {
    local name=$1
    local value=$2

    if ! awk -v value="${value}" 'BEGIN { exit !(value ~ /^[0-9]+([.][0-9]+)?$/ && value >= 0 && value <= 100) }'; then
        echo "${name} must be a number between 0 and 100: ${value}" >&2
        exit 2
    fi
}

function require_nonnegative_number() {
    local name=$1
    local value=$2

    if ! [[ "${value}" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
        echo "${name} must be a non-negative number: ${value}" >&2
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
    local ns

    for ns in C0 C1; do
        sudo ip netns exec "${ns}" pkill -x iperf >/dev/null 2>&1 || true
    done
    for _ in $(seq 1 20); do
        if ! sudo ip netns exec C0 pgrep -x iperf >/dev/null 2>&1 && \
            ! sudo ip netns exec C1 pgrep -x iperf >/dev/null 2>&1; then
            return
        fi
        sleep 0.1
    done
    echo "Timed out waiting for iperf servers to stop" >&2
    exit 1
}

function read_lb_stat() {
    sudo ip netns exec LB cat "/sys/class/net/net0/statistics/$1"
}

function read_forwarded_packets() {
    local total=0
    local ns

    for ns in C0 C1; do
        total=$((total + $(sudo ip netns exec "${ns}" \
            cat /sys/class/net/v6tun0/statistics/rx_packets)))
    done
    echo "${total}"
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
    for _ in $(seq 1 20); do
        if sudo ip netns exec C0 pgrep -x iperf >/dev/null 2>&1 && \
            sudo ip netns exec C1 pgrep -x iperf >/dev/null 2>&1; then
            return
        fi
        sleep 0.1
    done
    echo "Timed out waiting for iperf servers to start" >&2
    exit 1
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
    local target_rate=$4
    local log_prefix=$5
    local status=0
    local pcap="${TMP_DIR}/${family}.pcap"
    local pids=()

    for index in $(seq 1 "${PARALLEL}"); do
        sudo ip netns exec U tcpreplay --intf1=net0 \
            --pps "$(((target_rate + PARALLEL - 1) / PARALLEL))" \
            --loop=0 --duration="${duration}" "${pcap}" \
            >"${TMP_DIR}/${log_prefix}-packet-generator-${index}.log" 2>&1 &
        pids+=("$!")
    done

    for index in "${!pids[@]}"; do
        if wait "${pids[$index]}"; then
            continue
        else
            status=$?
        fi
        if [ "${status}" -ne 0 ]; then
            cat "${TMP_DIR}/${log_prefix}-packet-generator-$((index + 1)).log" >&2
            return "${status}"
        fi
    done
}

function run_case() {
    local family=$1
    local vip=$2
    local profile=$3
    local target_rate=$4
    local repetition=$5
    local case_name="${family}-${profile}-${target_rate}-${repetition}"
    local output="${TMP_DIR}/${case_name}.csv"
    local packets_before bytes_before drops_before packets_after bytes_after drops_after
    local forwarded_before forwarded_after
    local time_before time_after elapsed_ns
    local cpu_total_before cpu_idle_before cpu_total_after cpu_idle_after
    local cpu_total_delta cpu_idle_delta
    local iperf_bits_per_second
    local l4lb_rss_kib l4lb_peak_rss_kib

    echo "Running ${family} ${profile} benchmark (target=${target_rate}, repetition=${repetition})..." >&2
    if [ "${profile}" = "throughput" ]; then
        start_servers "${family}" "${vip}"
    else
        stop_servers
        run_packet_generator "${family}" "${vip}" "${WARMUP_DURATION}" \
            "${target_rate}" "${case_name}-warmup"
    fi

    packets_before=$(read_lb_stat rx_packets)
    bytes_before=$(read_lb_stat rx_bytes)
    drops_before=$(read_lb_stat rx_dropped)
    forwarded_before=$(read_forwarded_packets)
    read -r cpu_total_before cpu_idle_before < <(read_cpu_stat)
    time_before=$(date +%s%N)

    if [ "${profile}" = "throughput" ]; then
        run_client "${family}" "${vip}" "${DURATION}" "${output}"
    else
        run_packet_generator "${family}" "${vip}" "${DURATION}" \
            "${target_rate}" "${case_name}"
    fi

    time_after=$(date +%s%N)
    read -r cpu_total_after cpu_idle_after < <(read_cpu_stat)
    # Allow packets already in the virtual interfaces to reach the tunnel counters.
    sleep 0.1
    packets_after=$(read_lb_stat rx_packets)
    bytes_after=$(read_lb_stat rx_bytes)
    drops_after=$(read_lb_stat rx_dropped)
    forwarded_after=$(read_forwarded_packets)
    stop_servers

    l4lb_rss_kib=$(read_l4lb_memory_kib VmRSS)
    l4lb_peak_rss_kib=$(read_l4lb_memory_kib VmHWM)

    elapsed_ns=$((time_after - time_before))
    if [ "${profile}" = "throughput" ] && [ "${elapsed_ns}" -lt "$((DURATION * 800000000))" ]; then
        echo "iperf finished too early for ${family}: ${elapsed_ns}ns" >&2
        cat "${output}.stderr" >&2
        exit 1
    fi
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
        -v target_pps="${target_rate}" \
        -v repetition="${repetition}" \
        -v parallel="${PARALLEL}" \
        -v requested_duration="${DURATION}" \
        -v elapsed_ns="${elapsed_ns}" \
        -v packets="$((packets_after - packets_before))" \
        -v bytes="$((bytes_after - bytes_before))" \
        -v drops="$((drops_after - drops_before))" \
        -v forwarded_packets="$((forwarded_after - forwarded_before))" \
        -v max_drop_percent="${MAX_FORWARDING_DROP_PERCENT}" \
        -v min_target_percent="${MIN_TARGET_ACHIEVEMENT_PERCENT}" \
        -v max_target_percent="${MAX_TARGET_ACHIEVEMENT_PERCENT}" \
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
            if (profile == "packet-rate") {
                seconds = requested_duration
            }
            pps = packets / seconds
            ingress_gbps = bytes * 8 / seconds / 1000000000
            tcp_throughput_gbps = ""
            target_achievement_percent = ""
            forwarding_drop_percent = 0
            forwarding_dropped_packets = packets - forwarded_packets
            if (forwarding_dropped_packets < 0) {
                forwarding_dropped_packets = 0
            }
            if (packets > 0) {
                forwarding_drop_percent = forwarding_dropped_packets * 100 / packets
            }
            sustainable = ""
            if (profile == "throughput") {
                tcp_throughput_gbps = sprintf("%.6f", iperf_bps / 1000000000)
            } else {
                target_achievement_percent = sprintf("%.2f", pps * 100 / target_pps)
                sustainable = (forwarding_drop_percent < max_drop_percent && \
                    pps * 100 / target_pps >= min_target_percent && \
                    pps * 100 / target_pps <= max_target_percent) ? "yes" : "no"
            }
            cpu = 0
            if (cpu_total_delta > 0) {
                cpu = (cpu_total_delta - cpu_idle_delta) * 100 / cpu_total_delta
            }
            printf "%s,%s,%d,%s,%.3f,%d,%d,%.0f,%s,%d,%d,%.4f,%s,%.6f,%s,%d,%.2f,%d,%d,%d,%d,%d,%d\n", \
                family, profile, repetition, target_pps, seconds, parallel, packets, pps, \
                target_achievement_percent, forwarded_packets, forwarding_dropped_packets, \
                forwarding_drop_percent, sustainable, ingress_gbps, tcp_throughput_gbps, drops, cpu, \
                l4lb_rss_kib, l4lb_peak_rss_kib, l4lb_binary_bytes, \
                bpf_program_memlock_bytes, bpf_maps_memlock_bytes, bpf_jited_bytes
        }'
}

require_positive_integer DURATION "${DURATION}"
require_positive_integer PARALLEL "${PARALLEL}"
require_positive_integer PORT "${PORT}"
require_positive_integer REPETITIONS "${REPETITIONS}"
require_positive_integer WARMUP_DURATION "${WARMUP_DURATION}"
require_percentage MAX_FORWARDING_DROP_PERCENT "${MAX_FORWARDING_DROP_PERCENT}"
require_percentage MIN_TARGET_ACHIEVEMENT_PERCENT "${MIN_TARGET_ACHIEVEMENT_PERCENT}"
require_nonnegative_number MAX_TARGET_ACHIEVEMENT_PERCENT "${MAX_TARGET_ACHIEVEMENT_PERCENT}"
if ! awk -v min="${MIN_TARGET_ACHIEVEMENT_PERCENT}" -v max="${MAX_TARGET_ACHIEVEMENT_PERCENT}" \
    'BEGIN { exit !(min <= max) }'; then
    echo "MIN_TARGET_ACHIEVEMENT_PERCENT must not exceed MAX_TARGET_ACHIEVEMENT_PERCENT" >&2
    exit 2
fi
if [ -z "${PACKET_RATES//[[:space:]]/}" ]; then
    echo "PACKET_RATES must contain at least one target rate" >&2
    exit 2
fi
for packet_rate in ${PACKET_RATES}; do
    require_positive_integer PACKET_RATES "${packet_rate}"
done

for command in awk bpftool date ip iperf jq pkill stat sudo tcpreplay; do
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
HEALTH_CHECK_ENABLED=false "${SCRIPT_DIR}/run-lb.sh" >"${TMP_DIR}/l4lb.log" 2>&1 &
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

u_mac=$(sudo ip netns exec U cat /sys/class/net/net0/address)
r_mac=$(sudo ip netns exec R cat /sys/class/net/netU/address)
go run "${SCRIPT_DIR}/benchmarkpkt" -family ipv4 \
    -src-ip 198.51.100.200 -dst-ip 192.0.2.10 \
    -src-mac "${u_mac}" -dst-mac "${r_mac}" -output "${TMP_DIR}/ipv4.pcap"
go run "${SCRIPT_DIR}/benchmarkpkt" -family ipv6 \
    -src-ip 2001:db8:0:2::200 -dst-ip 2001:db8:100::10 \
    -src-mac "${u_mac}" -dst-mac "${r_mac}" -output "${TMP_DIR}/ipv6.pcap"

echo "family,profile,repetition,target_pps,duration_seconds,parallel,ingress_packets,ingress_pps,target_achievement_percent,forwarded_packets,forwarding_dropped_packets,forwarding_drop_percent,sustainable,ingress_gbps,tcp_throughput_gbps,rx_dropped,host_cpu_busy_percent,l4lb_rss_kib,l4lb_peak_rss_kib,l4lb_binary_bytes,bpf_program_memlock_bytes,bpf_maps_memlock_bytes,bpf_jited_bytes"
for family in ipv4 ipv6; do
    if [ "${family}" = "ipv4" ]; then
        vip=192.0.2.10
    else
        vip=2001:db8:100::10
    fi
    for repetition in $(seq 1 "${REPETITIONS}"); do
        run_case "${family}" "${vip}" throughput 0 "${repetition}"
    done
    for packet_rate in ${PACKET_RATES}; do
        for repetition in $(seq 1 "${REPETITIONS}"); do
            run_case "${family}" "${vip}" packet-rate "${packet_rate}" "${repetition}"
        done
    done
done
