#!/bin/sh
# Smoke test for analysis, run by `make smoke` against a running stack. It
# runs a whole job for the power-delhi-monthly bundle, through ingest,
# preprocess and analysis, waits for it to succeed, then downloads the trend
# analysis through a signed URL and checks the regression, Mann-Kendall,
# extras and diagnostics results are in it.
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
if ! "$PY" -I deploy/smoke/watch.py "$id" "$api"; then
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
summary=$(curl -s "$url" | "$PY" -I -c '
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
# The extras, which the catalog plan asks for.
sh, tri, ys = a["shuffle"], a["trendTriangle"], a["yearsToSignificance"]
assert sh["permutations"] == len(sh["shuffledSlopesPerDecade"]) == 1000, "shuffle"
assert len(tri["slopesPerDecade"]) == len(tri["startYears"]) * len(tri["endYears"]), "trend triangle"
assert [m["month"] for m in a["monthlyTrends"]] == list(range(1, 13)), "monthly trends"
assert float(ys["yearsSoFar"]) == 44, ys
# The diagnostics.
pt, rc, ol = a["pettitt"], a["residualChecks"], a["outliers"]
assert "changeTime" in pt, "pettitt"
statuses = ("CHECK_STATUS_PASS", "CHECK_STATUS_WARN")
assert rc.get("autocorrelation") in statuses and rc.get("normality") in statuses, rc
assert float(ol["robustZThreshold"]) == 3.5 and "highest" in ol and "lowest" in ol, "outliers"
print("%+.3f %s per decade, regression p %.2g, Mann-Kendall p %.2g, shuffle p %.2g, %.0f years to significance, "
      "Pettitt change %s" % (
    float(reg.get("slopePerDecade", 0)), a.get("units", ""), float(reg.get("pValue", 0)), float(mk.get("pValue", 0)),
    float(sh.get("pValue", 0)), float(ys.get("yearsNeeded", 0)), pt["changeTime"][:7]))
')

echo "OK: job $id went through every stage: $summary"
