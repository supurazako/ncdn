#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cert_dir="${script_dir}/certs"

mkdir -p "${cert_dir}"
mkdir -p "${script_dir}/artifacts/qlog" "${script_dir}/artifacts/mlog"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
	-keyout "${cert_dir}/localhost.key" \
	-out "${cert_dir}/localhost.crt" \
	-days 10 \
	-subj "/CN=localhost" \
	-addext "subjectAltName=DNS:localhost,DNS:relay,IP:127.0.0.1,IP:::1"

echo "generated ${cert_dir}/localhost.crt"
