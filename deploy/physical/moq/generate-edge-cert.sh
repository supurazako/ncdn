#!/usr/bin/env bash
set -euo pipefail

output_dir="${1:-./certs}"
valid_days="${CERT_VALID_DAYS:-10}"

if [[ ! "${valid_days}" =~ ^[0-9]+$ ]] || ((valid_days < 1 || valid_days > 14)); then
  printf 'CERT_VALID_DAYS must be an integer from 1 to 14 for WebTransport\n' >&2
  exit 1
fi

mkdir -p "${output_dir}"
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout "${output_dir}/relay.key" \
  -out "${output_dir}/relay.crt" \
  -days "${valid_days}" \
  -subj "/CN=ncdn-moq-edge" \
  -addext "subjectAltName=IP:219.100.95.113,IP:2401:5e40:10ff:ff04::1"
chmod 600 "${output_dir}/relay.key"
openssl x509 -in "${output_dir}/relay.crt" -outform DER \
  | openssl dgst -sha256 -hex \
  | awk '{print tolower($2)}' \
  >"${output_dir}/relay.sha256"

printf 'certificate: %s\n' "${output_dir}/relay.crt"
printf 'fingerprint: %s\n' "$(tr -d '\n' <"${output_dir}/relay.sha256")"
