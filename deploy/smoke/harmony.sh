#!/bin/sh
# Live check of the harmony-subset adapter against real NASA services, run by
# `make smoke-harmony`. It needs EARTHDATA_TOKEN in .env, so CI doesn't run it.
#
# It asks the gateway for two years of GPM IMERG monthly rain over a small box
# near Delhi, stopping after ingest, and waits for the 24 NetCDF files.
# Harmony usually takes one to five minutes.
set -eu
. "$(dirname "$0")/python.sh"

if ! grep -qE '^EARTHDATA_TOKEN=.+' .env 2>/dev/null; then
  echo "SKIP: set EARTHDATA_TOKEN in .env first, then run make up"
  exit 1
fi

api=http://127.0.0.1:8080/trenddetective.gateway.v1.GatewayService
call() { curl -s -H 'Content-Type: application/json' -d "$2" "$api/$1"; }
field() { "$PY" -I -c '
import json, sys
value = json.load(sys.stdin)
for key in sys.argv[1].split("."):
    value = value[int(key)] if isinstance(value, list) else value.get(key, "")
print(value if not isinstance(value, (dict, list)) else json.dumps(value))
' "$1"; }

spec='{"spec": {"variableId": "precipitation-gpm",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 2000, "month": 1, "day": 1}, "end": {"year": 2001, "month": 12, "day": 31}},
  "stopAfter": "STAGE_INGEST"}}'
id=$(call CreateJob "$spec" | field job.id)
[ -n "$id" ] || { echo "FAIL: CreateJob returned no job"; exit 1; }
echo "Job $id: waiting for Harmony, up to 10 minutes"

state=""
for _ in $(seq 1 60); do
  job=$(call GetJob "{\"jobId\": \"$id\"}")
  state=$(printf '%s' "$job" | field job.state)
  case "$state" in JOB_STATE_SUCCEEDED|JOB_STATE_FAILED) break ;; esac
  sleep 10
done

if [ "$state" != JOB_STATE_SUCCEEDED ]; then
  echo "FAIL: job ended as '$state'"
  printf '%s' "$job" | field job.tasks.0.error
  docker compose logs --no-log-prefix --tail=20 ingest
  exit 1
fi
files=$(printf '%s' "$job" | "$PY" -I -c 'import json,sys; print(len(json.load(sys.stdin)["job"]["results"]["raw"][0]["files"]))')
echo "OK: Harmony delivered $files NetCDF files into the raw bucket"
