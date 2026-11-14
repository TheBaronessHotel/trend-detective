#!/bin/sh
# Smoke test for the gateway, run by `make smoke` against a running stack. It
# lists the catalog, then creates a job that stops after ingest and waits for
# it to succeed. That covers the whole loop: Connect API, Redis, the ingest
# task on NATS, ingest itself, and the StageDone coming back.
set -eu

api=http://127.0.0.1:8080/trenddetective.gateway.v1.GatewayService

call() {
  curl -s -H 'Content-Type: application/json' -d "$2" "$api/$1"
}

field() {
  python3 -I -c '
import json, sys
value = json.load(sys.stdin)
for key in sys.argv[1].split("."):
    value = value[int(key)] if isinstance(value, list) else value.get(key, "")
print(value if not isinstance(value, (dict, list)) else json.dumps(value))
' "$1"
}

echo "Waiting for the gateway"
for _ in $(seq 1 30); do
  if call ListVariables '{}' >/tmp/smoke-vars.json 2>/dev/null && grep -q air-temperature-power /tmp/smoke-vars.json; then
    break
  fi
  sleep 1
done
grep -q air-temperature-power /tmp/smoke-vars.json || { echo "FAIL: ListVariables never answered"; exit 1; }

echo "Creating a job that stops after ingest"
spec='{"spec": {"variableId": "air-temperature-power",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 1981, "month": 1, "day": 1}, "end": {"year": 2024, "month": 12, "day": 31}},
  "stopAfter": "STAGE_INGEST"}}'
created=$(call CreateJob "$spec")
id=$(printf '%s' "$created" | field job.id)
if [ -z "$id" ]; then
  echo "FAIL: CreateJob returned no job: $created"
  exit 1
fi

echo "Waiting for job $id"
state=""
for _ in $(seq 1 30); do
  job=$(call GetJob "{\"jobId\": \"$id\"}")
  state=$(printf '%s' "$job" | field job.state)
  case "$state" in JOB_STATE_SUCCEEDED|JOB_STATE_FAILED) break ;; esac
  sleep 1
done
if [ "$state" != JOB_STATE_SUCCEEDED ]; then
  echo "FAIL: job $id ended as '$state': $job"
  docker compose logs --no-log-prefix --tail=20 gateway ingest
  exit 1
fi
raw=$(printf '%s' "$job" | field job.results.raw.0.variables.0)
if [ "$raw" != T2M ]; then
  echo "FAIL: job $id has no raw dataset for T2M: $job"
  exit 1
fi

echo "Checking that a bad request is refused"
bad=$(call CreateJob '{"spec": {"variableId": "nope"}}')
case "$bad" in *invalid_argument*) ;; *) echo "FAIL: unknown variable wasn't refused: $bad"; exit 1 ;; esac

echo "OK: job $id went through the gateway and ingest and succeeded"
