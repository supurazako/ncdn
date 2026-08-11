#!/bin/sh
set -eu

base=${MOQ_GUI_URL:-http://localhost:3002}
relay=${MOQ_RELAY_HTTP_URL:-http://localhost:4443}
wait_seconds=${MOQ_VERIFY_WAIT_SECONDS:-35}
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

for service in relay publisher video-source visualizer; do
  id=$(docker compose ps -q "$service")
  test -n "$id"
  test "$(docker inspect -f '{{.State.Running}}' "$id")" = true
done

i=0
while ! curl -fsS "$relay/certificate.sha256" -o "$tmp_dir/certificate.sha256"; do
  i=$((i + 1))
  if test "$i" -ge "$wait_seconds"; then
    echo "relay certificate fingerprint was not ready" >&2
    exit 1
  fi
  sleep 1
done

grep -Eq '^[0-9a-fA-F]{64}$' "$tmp_dir/certificate.sha256"
curl -fsS "$base/" -o "$tmp_dir/index.html"
curl -fsS "$base/bootstrap.js" -o "$tmp_dir/bootstrap.js"
curl -fsS "$base/app.js" -o "$tmp_dir/app.js"

grep -F '<moq-watch' "$tmp_dir/index.html" >/dev/null
grep -F '@moq/watch@0.4.5' "$tmp_dir/bootstrap.js" >/dev/null
grep -F 'WebTransport' "$tmp_dir/app.js" >/dev/null
grep -F 'websocket = { enabled: false }' "$tmp_dir/app.js" >/dev/null

docker compose logs --no-color relay \
  | sed 's/\x1b\[[0-9;]*m//g' \
  | grep -F 'broadcast=demo.hang' >/dev/null

echo "services=ok"
echo "relay_fingerprint=ok"
echo "direct_player=ok"
echo "publisher_to_relay=ok"
echo "browser_webtransport=manual-check-required"
