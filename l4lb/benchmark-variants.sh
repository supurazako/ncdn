#!/bin/bash
set -euo pipefail

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
VARIANTS=${VARIANTS:-"full no-stats inline-dest pow2-dests keep-padding minimal"}
OUTPUT_DIR=${OUTPUT_DIR:-"/tmp/ncdn-l4lb-variants-$(date +%Y%m%d-%H%M%S)"}

mkdir -p "${OUTPUT_DIR}"

for variant in ${VARIANTS}; do
    case "${variant}" in
        full|no-stats|inline-dest|pow2-dests|keep-padding|minimal) ;;
        *)
            echo "Unknown variant: ${variant}" >&2
            exit 2
            ;;
    esac

    output="${OUTPUT_DIR}/${variant}.csv"
    echo "Benchmarking ${variant}; output=${output}" >&2
    L4LB_VARIANT="${variant}" "${SCRIPT_DIR}/benchmark.sh" >"${output}"
done

echo "Variant benchmark results: ${OUTPUT_DIR}" >&2
