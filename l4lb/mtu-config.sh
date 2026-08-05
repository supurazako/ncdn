#!/bin/bash

# The deployment may override this before invoking a script:
#
#   UNDERLAY_MTU=1492 sudo -E ./netns_setup.sh
#
# All other MTU and MSS values are derived from this one value.
UNDERLAY_MTU=${UNDERLAY_MTU:-1500}
OUTER_IPV6_HEADER_LEN=40

if [[ ! "${UNDERLAY_MTU}" =~ ^[0-9]+$ ]]; then
    echo "UNDERLAY_MTU must be an integer: ${UNDERLAY_MTU}" >&2
    return 1
fi

# IPv6 links require an MTU of at least 1280. Since the PoP transports an
# inner IPv6 packet inside a 40-byte outer IPv6 header, the underlay must carry
# at least 1320 bytes.
if ((UNDERLAY_MTU < 1320 || UNDERLAY_MTU > 65535)); then
    echo "UNDERLAY_MTU must be between 1320 and 65535: ${UNDERLAY_MTU}" >&2
    return 1
fi

INNER_MTU=$((UNDERLAY_MTU - OUTER_IPV6_HEADER_LEN))
IPV4_TCP_MSS=$((INNER_MTU - 20 - 20))
IPV6_TCP_MSS=$((INNER_MTU - 40 - 20))
