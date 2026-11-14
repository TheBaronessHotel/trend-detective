#!/bin/sh
# Smoke test for analysis, run by `make smoke` against a running stack. It
# runs a whole job for the power-delhi-monthly bundle, through ingest,
# preprocess and analysis, waits for it to succeed, then downloads the trend
# analysis through a signed URL and checks the regression and Mann-Kendall
# results are in it.
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

echo "Creating a job that runs every stage"
spec='{"spec": {"variableId": "air-temperature-power", "source": "SOURCE_BUNDLE",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 1981, "month": 1, "day": 1}, "end": {"year": 2024, "month": 12, "day": 31}}}}'
created=$(call CreateJob "$spec")
id=$(printf '%s' "$created" | field job.id)
if [ -z "$id" ]; then
  echo "FAIL: CreateJob returned no job: $created"
  exit 1
fi

echo "Watching job $id"
if ! python3 -I deploy/smoke/watch.py "$id" "$api"; then
  echo "FAIL: job $id didn't succeed: $(call GetJob "{\"jobId\": \"$id\"}")"
  docker compose logs --no-log-prefix --tail=30 analysis
  exit 1
fi

echo "Downloading the trend analysis"
url=$(call GetResultURL "{\"jobId\": \"$id\", \"artifact\": \"ARTIFACT_SERIES_ANALYSIS\"}" | field url)
if [ -z "$url" ]; then
  echo "FAIL: GetResultURL gave no URL for the trend analysis"
  exit 1
fi
summary=$(curl -s "$url" | python3 -I -c '
import json, math, sys
a = json.load(sys.stdin)
assert a["variableId"] == "air-temperature-power", a["variableId"]
assert a["nValid"] == 528, a["nValid"]
reg, mk = a["regression"], a["mannKendall"]
# protojson leaves out zeros, so a missing number is 0.
for name, value in [("regression slope", reg.get("slopePerDecade", 0)), ("regression p", reg.get("pValue", 0)),
                    ("Sen slope", mk.get("sensSlopePerDecade", 0)), ("Mann-Kendall p", mk.get("pValue", 0))]:
    assert math.isfinite(float(value)), (name, value)
assert "timeOrigin" in reg["model"], "the fitted model has no time origin"
print("%+.3f %s per decade, regression p %.2g, Mann-Kendall p %.2g" % (
    float(reg.get("slopePerDecade", 0)), a.get("units", ""), float(reg.get("pValue", 0)), float(mk.get("pValue", 0))))
')

echo "OK: job $id went through every stage: $summary"
