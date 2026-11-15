#!/bin/sh
# Smoke test for the gateway, run by `make smoke` against a running stack. It
# lists the catalog, then creates a job that stops after ingest and waits for
# it to succeed. That covers the whole loop: Connect API, Redis, the ingest
# task on NATS, ingest itself, and the StageDone coming back.
set -eu
. "$(dirname "$0")/python.sh"

api=http://127.0.0.1:8080/trenddetective.gateway.v1.GatewayService

call() {
  curl -s -H 'Content-Type: application/json' -d "$2" "$api/$1"
}

field() {
  "$PY" -I -c '
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
# The bundle source keeps CI independent of NASA's servers. make smoke-power
# checks the live POWER adapter.
spec='{"spec": {"variableId": "air-temperature-power", "source": "SOURCE_BUNDLE",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 1981, "month": 1, "day": 1}, "end": {"year": 2024, "month": 12, "day": 31}},
  "stopAfter": "STAGE_INGEST"}}'
created=$(call CreateJob "$spec")
id=$(printf '%s' "$created" | field job.id)
if [ -z "$id" ]; then
  echo "FAIL: CreateJob returned no job: $created"
  exit 1
fi

echo "Watching job $id"
"$PY" -I deploy/smoke/watch.py "$id" "$api" || { echo "FAIL: WatchJob didn't end with the job succeeded"; exit 1; }

echo "Checking job $id"
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

status=$(curl -s -o /tmp/smoke-debug.json -w '%{http_code}' "http://127.0.0.1:8080/debug/jobs/$id")
if [ "$status" != 200 ] || ! grep -q stage_done_events /tmp/smoke-debug.json; then
  echo "FAIL: /debug/jobs/$id answered $status"
  exit 1
fi

echo "Checking that a job that stopped after ingest has no time series"
notready=$(call GetResultURL "{\"jobId\": \"$id\", \"artifact\": \"ARTIFACT_SERIES\"}")
case "$notready" in *failed_precondition*) ;; *) echo "FAIL: GetResultURL should say there's no series: $notready"; exit 1 ;; esac

# upload FILE: uploads FILE through a signed URL and prints its BlobRef.
upload() {
  up=$(call CreateUploadURL "{\"filename\": \"smoke.csv\", \"sizeBytes\": $(wc -c < "$1" | tr -d ' ')}")
  status=$(curl -s -o /dev/null -w '%{http_code}' -X PUT --data-binary @"$1" "$(printf '%s' "$up" | field url)")
  if [ "$status" != 200 ]; then
    echo "FAIL: upload through the signed URL answered $status" >&2
    exit 1
  fi
  printf '%s' "$up" | field upload
}

# csv_job REF: creates a job on an uploaded file that stops after ingest,
# waits for it to end and prints its ID.
csv_job() {
  created=$(call CreateJob "{\"spec\": {\"variableId\": \"user-csv\", \"upload\": $1,
    \"bbox\": {\"west\": -9.5, \"south\": 38.5, \"east\": -9.0, \"north\": 39.0},
    \"dates\": {\"start\": {\"year\": 1800, \"month\": 1, \"day\": 1}, \"end\": {\"year\": 2100, \"month\": 12, \"day\": 31}},
    \"stopAfter\": \"STAGE_INGEST\"}}")
  job=$(printf '%s' "$created" | field job.id)
  [ -n "$job" ] || { echo "FAIL: CreateJob refused the CSV job: $created" >&2; exit 1; }
  "$PY" -I deploy/smoke/watch.py "$job" "$api" >/dev/null 2>&1 || true
  printf '%s' "$job"
}

echo "Uploading a CSV through a signed URL and running a job on it"
{
  echo 'date,rain (mm)'
  for y in 2001 2002 2003; do
    for m in 01 02 03 04 05 06 07 08 09 10 11 12; do echo "$y-$m,$m.5"; done
  done
} > /tmp/smoke-upload.csv
ref=$(upload /tmp/smoke-upload.csv)
csvid=$(csv_job "$ref")
raw=$(call GetJob "{\"jobId\": \"$csvid\"}" | "$PY" -I -c '
import json, sys
job = json.load(sys.stdin)["job"]
raws = job.get("results", {}).get("raw", [])
if job["state"] != "JOB_STATE_SUCCEEDED" or not raws:
    sys.exit("job ended as %s: %s" % (job["state"], json.dumps(job.get("tasks"))))
raw = raws[0]
if raw["format"] != "FORMAT_USER_CSV" or raw.get("step") != "TIME_STEP_MONTHLY" or raw["variables"] != ["rain (mm)"]:
    sys.exit("unexpected raw dataset: " + json.dumps(raw))
print(raw["files"][0]["blob"]["key"])
') || { echo "FAIL: CSV job $csvid: $raw"; exit 1; }
clean=$(curl -s "http://127.0.0.1:8888/buckets/raw/$raw" | head -2 | tr '\n' ' ')
if [ "$clean" != "date,value 2001-01-01,1.5 " ]; then
  echo "FAIL: the cleaned-up CSV starts with '$clean'"
  exit 1
fi
curl -s -o /dev/null -X DELETE "http://127.0.0.1:8888/buckets/uploads/$(printf '%s' "$ref" | field key)"

echo "Checking that a broken CSV fails with the row to fix"
printf 'date,value\n2001-01,1\n2001-02,abc\n' > /tmp/smoke-bad.csv
ref=$(upload /tmp/smoke-bad.csv)
badid=$(csv_job "$ref")
message=$(call GetJob "{\"jobId\": \"$badid\"}" | field job.tasks.0.error.userMessage)
case "$message" in *'Row 3: "abc"'*) ;; *) echo "FAIL: broken CSV job $badid said '$message'"; exit 1 ;; esac
curl -s -o /dev/null -X DELETE "http://127.0.0.1:8888/buckets/uploads/$(printf '%s' "$ref" | field key)"

echo "Checking that dates outside the variable's range are refused"
outside=$(call CreateJob '{"spec": {"variableId": "air-temperature-power", "source": "SOURCE_BUNDLE",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 1950, "month": 1, "day": 1}, "end": {"year": 1960, "month": 12, "day": 31}}}}')
case "$outside" in *"has data from"*) ;; *) echo "FAIL: dates before 1981 weren't refused: $outside"; exit 1 ;; esac

echo "Checking that a bad request is refused"
bad=$(call CreateJob '{"spec": {"variableId": "nope"}}')
case "$bad" in *invalid_argument*) ;; *) echo "FAIL: unknown variable wasn't refused: $bad"; exit 1 ;; esac

echo "OK: job $id went through the gateway and ingest and succeeded"
