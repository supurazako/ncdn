#!/usr/bin/env bash
set -euo pipefail

# Measure an already running popcache. The default endpoint is the local
# development setup; override it and the workload with environment variables.
URL="${URL:-http://127.0.0.1:8889/json}"
REQUESTS="${REQUESTS:-20000}"
CONCURRENCIES="${CONCURRENCIES:-${CONCURRENCY:-100}}"
RUNS="${RUNS:-3}"
OUTPUT_DIR="${OUTPUT_DIR:-/tmp/ncdn-http-benchmark}"

if ! command -v ab >/dev/null 2>&1; then
	echo "ab (ApacheBench) is required" >&2
	exit 1
fi

mkdir -p "${OUTPUT_DIR}"

echo "warming cache: ${URL}"
curl --fail --silent --show-error "${URL}" >/dev/null
echo "URL=${URL} requests=${REQUESTS} concurrencies=${CONCURRENCIES} runs=${RUNS}"

for concurrency in ${CONCURRENCIES//,/ }; do
	for run in $(seq 1 "${RUNS}"); do
		result_file="${OUTPUT_DIR}/c${concurrency}-run-${run}.csv"
		echo
		echo "concurrency=${concurrency} run ${run}/${RUNS} (percentile data: ${result_file})"
		ab -k -n "${REQUESTS}" -c "${concurrency}" -e "${result_file}" "${URL}"
	done
done
