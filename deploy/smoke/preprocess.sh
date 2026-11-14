#!/bin/sh
# Smoke test for preprocess, run by `make smoke` against a running stack. It
# creates a job for the power-delhi-monthly bundle that stops after
# preprocess, waits for it to succeed, then downloads the Series through a
# signed URL and checks it has every month from 1981 to 2024, with the
# anomalies, normals and display line the catalog's filter chain asks for.
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

echo "Creating a job that stops after preprocess"
spec='{"spec": {"variableId": "air-temperature-power", "source": "SOURCE_BUNDLE",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 1981, "month": 1, "day": 1}, "end": {"year": 2024, "month": 12, "day": 31}},
  "stopAfter": "STAGE_PREPROCESS"}}'
created=$(call CreateJob "$spec")
id=$(printf '%s' "$created" | field job.id)
if [ -z "$id" ]; then
  echo "FAIL: CreateJob returned no job: $created"
  exit 1
fi

echo "Watching job $id"
if ! python3 -I deploy/smoke/watch.py "$id" "$api"; then
  echo "FAIL: job $id didn't succeed: $(call GetJob "{\"jobId\": \"$id\"}")"
  docker compose logs --no-log-prefix --tail=30 preprocess
  exit 1
fi

echo "Downloading the series"
url=$(call GetResultURL "{\"jobId\": \"$id\", \"artifact\": \"ARTIFACT_SERIES\"}" | field url)
if [ -z "$url" ]; then
  echo "FAIL: GetResultURL gave no URL for the series"
  exit 1
fi
count=$(curl -s "$url" | python3 -I -c '
import json, sys
s = json.load(sys.stdin)
n = len(s["times"])
for field in ("values", "coverage", "anomalies", "displayValues"):
    assert len(s.get(field, [])) == n, f"{field} has {len(s.get(field, []))} entries, want {n}"
assert s["variableId"] == "air-temperature-power", s["variableId"]
assert s["anomalyKind"] == "ANOMALY_KIND_ABSOLUTE", s.get("anomalyKind")
assert len(s["normals"]) == 12 and all(x.get("usable") for x in s["normals"]), "want 12 usable normals"
print(n)
')
if [ "$count" != 528 ]; then
  echo "FAIL: the series has $count values, want 528"
  exit 1
fi

csv=$(call GetResultURL "{\"jobId\": \"$id\", \"artifact\": \"ARTIFACT_SERIES_CSV\"}" | field url)
lines=$(curl -s "$csv" | wc -l | tr -d ' ')
if [ "$lines" != 529 ]; then
  echo "FAIL: the series CSV has $lines lines, want 529"
  exit 1
fi

echo "OK: job $id went through ingest and preprocess, and its series has 528 months"
