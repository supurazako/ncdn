#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$script_dir"

base=${MOQ_GUI_URL:-http://localhost:3002}
edge_c0=${MOQ_EDGE_C0_HTTP_URL:-http://localhost:4443}
edge_c1=${MOQ_EDGE_C1_HTTP_URL:-http://localhost:4444}
wait_seconds=${MOQ_VERIFY_WAIT_SECONDS:-45}
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

services="origin-relay edge-c0 edge-c1 publisher-motion source-motion publisher-bars source-bars router visualizer"
for service in $services; do
  id=$(docker compose ps -q "$service")
  test -n "$id"
  test "$(docker inspect -f '{{.State.Running}}' "$id")" = true
done

wait_for_url() {
  url=$1
  output=$2
  count=0
  while ! curl -fsS "$url" -o "$output"; do
    count=$((count + 1))
    if test "$count" -ge "$wait_seconds"; then
      echo "timed out waiting for $url" >&2
      exit 1
    fi
    sleep 1
  done
}

wait_for_url "$edge_c0/certificate.sha256" "$tmp_dir/edge-c0.sha256"
wait_for_url "$edge_c1/certificate.sha256" "$tmp_dir/edge-c1.sha256"
wait_for_url "$base/api/channels" "$tmp_dir/channels.json"
wait_for_url "$base/api/config" "$tmp_dir/config.json"
wait_for_url "$base/" "$tmp_dir/index.html"
wait_for_url "$base/bootstrap.js" "$tmp_dir/bootstrap.js"
wait_for_url "$base/app.js" "$tmp_dir/app.js"
wait_for_url "$base/distribution.html" "$tmp_dir/distribution.html"
wait_for_url "$base/distribution.js" "$tmp_dir/distribution.js"
wait_for_url "$base/vendor/watch-element.js" "$tmp_dir/watch-element.js"

grep -Eq '^[0-9a-fA-F]{64}$' "$tmp_dir/edge-c0.sha256"
grep -Eq '^[0-9a-fA-F]{64}$' "$tmp_dir/edge-c1.sha256"
grep -F 'motion.hang' "$tmp_dir/channels.json" >/dev/null
grep -F 'bars.hang' "$tmp_dir/channels.json" >/dev/null
grep -F 'moq_url' "$tmp_dir/config.json" >/dev/null
grep -F 'group_duration_seconds' "$tmp_dir/config.json" >/dev/null
grep -F './vendor/watch-element.js' "$tmp_dir/bootstrap.js" >/dev/null
grep -F 'createElement("moq-watch")' "$tmp_dir/app.js" >/dev/null
grep -F 'websocket = { enabled: false }' "$tmp_dir/app.js" >/dev/null
grep -F '10秒戻る' "$tmp_dir/index.html" >/dev/null
grep -F '__NCDN_REWIND_START_GROUP' "$tmp_dir/watch-element.js" >/dev/null
test "$(grep -o 'ncdn-moq-group' "$tmp_dir/watch-element.js" | wc -l)" -eq 1
grep -F 'PoP Distribution' "$tmp_dir/distribution.html" >/dev/null
grep -F '/api/distribution' "$tmp_dir/distribution.js" >/dev/null

wait_for_url "$base/api/route?namespace=motion.hang&strategy=rendezvous" "$tmp_dir/motion-route.json"
wait_for_url "$base/api/route?namespace=bars.hang&strategy=rendezvous" "$tmp_dir/bars-route.json"
grep -F 'edge-' "$tmp_dir/motion-route.json" >/dev/null
grep -F 'edge-' "$tmp_dir/bars-route.json" >/dev/null

docker compose logs --no-color publisher-motion \
  | sed 's/\x1b\[[0-9;]*m//g' \
  | grep -F 'path=motion.hang' >/dev/null
docker compose logs --no-color publisher-bars \
  | sed 's/\x1b\[[0-9;]*m//g' \
  | grep -F 'path=bars.hang' >/dev/null
for edge in edge-c0 edge-c1; do
  docker compose logs --no-color "$edge" \
    | sed 's/\x1b\[[0-9;]*m//g' \
    | grep -F 'connected version=moq-lite-' >/dev/null
done

compose_network=$(docker inspect -f '{{range $name, $_ := .NetworkSettings.Networks}}{{$name}}{{end}}' \
  "$(docker compose ps -q edge-c0)")

verify_media() {
  relay=$1
  broadcast=$2
  output=$3
  if timeout --signal=INT 6s docker run --rm --network "$compose_network" \
    moqdev/moq-cli:0.9.9 \
    --client-connect "http://$relay:4443" \
    --broadcast "$broadcast" export h264 >"$output" 2>"$output.log"; then
    :
  else
    code=$?
    # timeout intentionally ends the live subscription after collecting frames.
    test "$code" -eq 124
  fi
  test -s "$output"
}

verify_media edge-c0 motion.hang "$tmp_dir/motion.h264"
verify_media edge-c1 bars.hang "$tmp_dir/bars.h264"

echo "services=ok"
echo "relay_cluster=ok"
echo "channels=motion.hang,bars.hang"
echo "media_flow=ok"
echo "shared_vip_config=ok"
echo "distribution_visualizer=ok"
echo "rewind_player=ok"
echo "routing_experiment=ok"
echo "browser_webtransport=manual-check-required"
