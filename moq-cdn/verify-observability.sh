#!/bin/sh
set -eu

base=${MOQ_HLS_URL:-http://localhost:8089/demo.hang}
wait_seconds=${MOQ_VERIFY_WAIT_SECONDS:-35}
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

for service in relay publisher hls-exporter video-source; do
  id=$(docker compose ps -q "$service")
  test -n "$id"
  test "$(docker inspect -f '{{.State.Running}}' "$id")" = true
done

i=0
while ! curl -fsS "$base/master.m3u8" -o "$tmp_dir/master.m3u8"; do
  i=$((i + 1))
  if test "$i" -ge "$wait_seconds"; then
    echo "master playlist was not ready" >&2
    exit 1
  fi
  sleep 1
done

media=$(awk '!/^#/ && NF { print; exit }' "$tmp_dir/master.m3u8")
test -n "$media"
curl -fsS "$base/$media" -o "$tmp_dir/media.m3u8"

segments=$(awk '!/^#/ && NF { count++ } END { print count+0 }' "$tmp_dir/media.m3u8")
test "$segments" -ge 2
oldest=$(awk '!/^#/ && NF { print; exit }' "$tmp_dir/media.m3u8")
group=$(printf '%s\n' "$oldest" | sed -n 's#.*seg/\([0-9][0-9]*\)\.m4s#\1#p')
test -n "$group"

media_dir=$(dirname "$media")
curl -fsS "$base/$media_dir/$oldest" -o "$tmp_dir/oldest.m4s"
test -s "$tmp_dir/oldest.m4s"

docker compose logs --no-color relay \
  | sed 's/\x1b\[[0-9;]*m//g' \
  | grep -F "group=$group" \
  | grep -F "fetch started" >/dev/null

echo "master=ok"
echo "dvr_segments=$segments"
echo "oldest_group=$group"
echo "oldest_group_bytes=$(wc -c < "$tmp_dir/oldest.m4s" | tr -d ' ')"
echo "relay_fetch=ok"
