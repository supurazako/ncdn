#!/bin/bash
set -euo pipefail

SCRIPT_DIR=$(readlink -f "$(dirname "$0")")
FORWARDING_TESTS='TestL4LBIPv4InIPv6|TestL4LBIPv6InIPv6'

make -C "${SCRIPT_DIR}/c" variants

for variant in full no-stats inline-dest pow2-dests keep-padding minimal; do
    echo "Building and verifying ${variant}..."
    cp "${SCRIPT_DIR}/c/lb-${variant}.o" "${SCRIPT_DIR}/c/lb.o"
    if [ "${variant}" = "full" ] || [ "${variant}" = "inline-dest" ] || \
        [ "${variant}" = "pow2-dests" ] || [ "${variant}" = "keep-padding" ]; then
        L4LB_VARIANT="${variant}" go test \
            -exec "sudo --preserve-env=L4LB_VARIANT" \
            "${SCRIPT_DIR}/l4lbdrv"
    else
        L4LB_VARIANT="${variant}" go test \
            -exec "sudo --preserve-env=L4LB_VARIANT" \
            "${SCRIPT_DIR}/l4lbdrv" -run "${FORWARDING_TESTS}"
    fi
done

cp "${SCRIPT_DIR}/c/lb-full.o" "${SCRIPT_DIR}/c/lb.o"
echo "All L4LB variants passed their expected correctness checks."
