#!/bin/sh
# Live check of the AIRS map variables against real NASA services, run by
# `make smoke-airs`. It needs EARTHDATA_TOKEN in .env, so CI doesn't run it.
#
# For each variable it asks the gateway for two years of AIRS3STM over a small
# box near Delhi and runs every stage. Then it downloads the region series and
# checks it has a value for most of the 24 months, in a range that variable
# can take. methane-airs covers the column_average step, and the two-variable
# Harmony request it needs. Pass variable IDs to check only those, for example
#   sh deploy/smoke/airs.sh methane-airs
# Each Harmony job takes one to five minutes.
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

# Variable ID and the range its monthly values can take, in display units.
ranges="water-vapour-airs:1:70
surface-air-temperature-airs:-20:45
skin-temperature-airs:-20:60
cloud-fraction-airs:0:100
ozone-airs:200:450
methane-airs:1750:1950"

want="${*:-$(printf '%s\n' "$ranges" | cut -d: -f1)}"
failed=0
for vid in $want; do
  range=$(printf '%s\n' "$ranges" | grep "^$vid:" || true)
  [ -n "$range" ] || { echo "FAIL: no range for $vid"; failed=1; continue; }
  lo=$(printf '%s' "$range" | cut -d: -f2)
  hi=$(printf '%s' "$range" | cut -d: -f3)

  spec="{\"spec\": {\"variableId\": \"$vid\",
    \"bbox\": {\"west\": 76, \"south\": 27, \"east\": 79, \"north\": 30},
    \"dates\": {\"start\": {\"year\": 2019, \"month\": 1, \"day\": 1}, \"end\": {\"year\": 2020, \"month\": 12, \"day\": 31}}}}"
  id=$(call CreateJob "$spec" | field job.id)
  if [ -z "$id" ]; then
    echo "FAIL: $vid: CreateJob returned no job"; failed=1; continue
  fi
  echo "$vid: job $id, waiting for Harmony and every stage, up to 10 minutes"

  state=""
  for _ in $(seq 1 60); do
    job=$(call GetJob "{\"jobId\": \"$id\"}")
    state=$(printf '%s' "$job" | field job.state)
    case "$state" in JOB_STATE_SUCCEEDED|JOB_STATE_FAILED) break ;; esac
    sleep 10
  done
  if [ "$state" != JOB_STATE_SUCCEEDED ]; then
    echo "FAIL: $vid: job ended as '$state'"
    printf '%s' "$job" | "$PY" -I -c '
import json, sys
for t in json.load(sys.stdin)["job"].get("tasks", []):
    if t.get("error"): print("  ", t.get("stage"), t["error"])'
    failed=1; continue
  fi

  url=$(call GetResultURL "{\"jobId\": \"$id\", \"artifact\": \"ARTIFACT_SERIES\"}" | field url)
  if ! curl -s "$url" | "$PY" -I -c '
import json, math, sys
vid, lo, hi = sys.argv[1], float(sys.argv[2]), float(sys.argv[3])
s = json.load(sys.stdin)
vals = [v for v in s["values"] if isinstance(v, (int, float)) and math.isfinite(v)]
n = len(s["times"])
assert n == 24, f"{n} months, want 24"
assert len(vals) >= 20, f"only {len(vals)} of {n} months have a value"
assert lo <= min(vals) and max(vals) <= hi, f"values {min(vals):.4g} to {max(vals):.4g}, want {lo:g} to {hi:g}"
assert len(s.get("anomalies", [])) == n, "no anomalies"
units = s.get("units", "")
print(f"OK: {vid}: {len(vals)} of {n} months, {min(vals):.4g} to {max(vals):.4g} {units}")
' "$vid" "$lo" "$hi"; then
    echo "FAIL: $vid: the series doesn't look right"; failed=1
  fi
done
exit $failed
