#!/usr/bin/env bash
set -euo pipefail

c0_metrics_url="${C0_METRICS_URL:-http://127.0.0.1:9091/metrics}"
c1_metrics_url="${C1_METRICS_URL:-http://127.0.0.1:9093/metrics}"
origin_metrics_url="${ORIGIN_METRICS_URL:-http://127.0.0.1:9092/metrics}"
c0_metrics="$(curl --fail --silent --show-error "${c0_metrics_url}")"
c1_metrics="$(curl --fail --silent --show-error "${c1_metrics_url}")"
origin_metrics="$(curl --fail --silent --show-error "${origin_metrics_url}")"

check_positive() {
	local role="$1"
	local metrics="$2"
	local metric="$3"
	local value
	value="$(awk -v metric="${metric}" '$1 == metric { print $2; exit }' <<<"${metrics}")"
	if [[ -z "${value}" ]] || ! awk -v value="${value}" 'BEGIN { exit !(value > 0) }'; then
		echo "${metric}: expected a value greater than zero, got ${value:-missing}" >&2
		exit 1
	fi
	echo "${role}.${metric}=${value}"
}

for edge in c0 c1; do
	metrics_var="${edge}_metrics"
	metrics="${!metrics_var}"
	check_positive "${edge}" "${metrics}" moq_relay_active_connections
	check_positive "${edge}" "${metrics}" moq_relay_upstream_connections
	check_positive "${edge}" "${metrics}" moq_relay_active_subscriptions
	check_positive "${edge}" "${metrics}" moq_relay_active_tracks
done

check_positive origin "${origin_metrics}" moq_relay_active_connections
check_positive origin "${origin_metrics}" moq_relay_active_publishers
check_positive origin "${origin_metrics}" moq_relay_active_subscriptions
check_positive origin "${origin_metrics}" moq_relay_active_tracks
