#!/bin/sh
# Smoke test for ingest, run by `make smoke` against a running stack. It
# publishes an ingest task for the power-delhi-monthly bundle, waits for the
# StageDone, and checks the dataset manifest landed in the raw bucket.
set -eu

network=trend-detective_default
box="docker run --rm -i --network $network natsio/nats-box:latest"
id="smoke-$(date +%s)"

task=$(cat <<EOF
{"meta": {"jobId": "$id", "taskId": "$id-ingest-0", "stage": "STAGE_INGEST"},
 "ingest": {"variableId": "power-t2m",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 1981, "month": 1, "day": 1}, "end": {"year": 2024, "month": 12, "day": 31}},
  "bundle": {"name": "power-delhi-monthly"}}}
EOF
)

echo "Publishing task $id-ingest-0"
printf '%s' "$task" | $box nats -s nats://nats:4222 pub --jetstream -H "Nats-Msg-Id:$id-ingest-0" tasks.ingest >/dev/null

echo "Waiting for its StageDone"
done_msg=""
for _ in $(seq 1 30); do
  done_msg=$($box nats -s nats://nats:4222 stream get EVENTS --last-for events.stage.done 2>/dev/null || true)
  case "$done_msg" in *"$id-ingest-0"*) break ;; esac
  done_msg=""
  sleep 1
done
if [ -z "$done_msg" ]; then
  echo "FAIL: no StageDone for $id-ingest-0 within 30s"
  docker compose logs --no-log-prefix --tail=20 ingest
  exit 1
fi
case "$done_msg" in
  *'"error"'*) echo "FAIL: ingest reported an error: $done_msg"; exit 1 ;;
esac

manifest=$(printf '%s' "$done_msg" | sed -n 's/.*"key": *"\(bundle\/[0-9a-f]*\)\/files\/.*/\1/p' | head -1)
if [ -z "$manifest" ]; then
  echo "FAIL: StageDone has no file under bundle/: $done_msg"
  exit 1
fi
status=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:8888/buckets/raw/$manifest/dataset.json")
if [ "$status" != 200 ]; then
  echo "FAIL: raw/$manifest/dataset.json is missing (HTTP $status)"
  exit 1
fi

echo "OK: ingest wrote raw/$manifest and reported it"
