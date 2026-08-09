#!/usr/bin/env bash
set -euo pipefail

metrics_url="${METRICS_URL:-http://127.0.0.1:9091/metrics}"
metrics="$(curl --fail --silent --show-error "${metrics_url}")"

check_positive() {
	local metric="$1"
	local value
	value="$(awk -v metric="${metric}" '$1 == metric { print $2; exit }' <<<"${metrics}")"
	if [[ -z "${value}" ]] || ! awk -v value="${value}" 'BEGIN { exit !(value > 0) }'; then
		echo "${metric}: expected a value greater than zero, got ${value:-missing}" >&2
		exit 1
	fi
	echo "${metric}=${value}"
}

check_positive moq_relay_active_connections
check_positive moq_relay_active_publishers
check_positive moq_relay_active_subscriptions
check_positive moq_relay_active_tracks
