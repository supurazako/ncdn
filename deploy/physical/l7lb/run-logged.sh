#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
log_dir=${L7LB_LOG_DIR:-"${script_dir}/logs"}
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
log_file="${log_dir}/l7lb-${timestamp}.log"

mkdir -p "${log_dir}"
printf 'L7LB log: %s\n' "${log_file}"

"${script_dir}/l7lb" "$@" > >(tee -a "${log_file}") 2>&1
