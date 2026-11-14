#!/bin/sh
# Live check of the giovanni-point adapter against Giovanni's time-series
# API, run by `make smoke-giovanni`. It needs EARTHDATA_TOKEN in .env, and CI
# skips it so CI doesn't depend on NASA's servers.
#
# It asks for twenty years of daily groundwater at the middle of a box in
# Punjab, India, stopping after ingest. Giovanni answers in a few seconds and
# reports the grid cell it used, whose center should be within 0.125 degrees
# of the box's.
set -eu

api=http://127.0.0.1:8080/trenddetective.gateway.v1.GatewayService
call() { curl -s -H 'Content-Type: application/json' -d "$2" "$api/$1"; }

spec='{"spec": {"variableId": "groundwater-gldas", "source": "SOURCE_GIOVANNI_POINT",
  "bbox": {"west": 75.0, "south": 30.0, "east": 76.0, "north": 31.0},
  "dates": {"start": {"year": 2004, "month": 1, "day": 1}, "end": {"year": 2023, "month": 12, "day": 31}},
  "stopAfter": "STAGE_INGEST"}}'
id=$(call CreateJob "$spec" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["job"]["id"])')
echo "Job $id: fetching from Giovanni"
python3 -I deploy/smoke/watch.py "$id" "$api" || { echo "FAIL: job didn't succeed"; call GetJob "{\"jobId\": \"$id\"}"; exit 1; }

job=$(call GetJob "{\"jobId\": \"$id\"}")
key=$(printf '%s' "$job" | python3 -I -c '
import json, sys
raw = json.load(sys.stdin)["job"]["results"]["raw"][0]
point, files = raw["point"], raw["files"]
ok = raw["source"] == "SOURCE_GIOVANNI_POINT" and raw["format"] == "FORMAT_GIOVANNI_POINT_CSV" \
    and raw["variables"] == ["GLDAS_CLSM025_DA1_D_2_2_GWS_tavg"] and len(files) == 1 \
    and abs(point["lat"] - 30.5) <= 0.125 and abs(point["lon"] - 75.5) <= 0.125
if not ok:
    sys.exit("unexpected raw dataset: " + json.dumps(raw))
print(files[0]["blob"]["key"])
') || { echo "FAIL: the raw dataset isn't what the job asked for"; exit 1; }

curl -s "http://127.0.0.1:8888/buckets/raw/$key" | python3 -I -c '
import sys
lines = sys.stdin.read().splitlines()
undef = next(l.split(",", 1)[1] for l in lines if l.startswith("undef,"))
start = next(i for i, l in enumerate(lines) if l.startswith("Timestamp"))
rows = [l.split(",", 1)[1] for l in lines[start + 1:] if l]
valid = sum(1 for v in rows if v.strip() not in ("", undef))
# 2004 to 2023 is 7305 days.
if len(rows) != 7305 or valid < 7000:
    sys.exit("FAIL: %d rows, %d with a value" % (len(rows), valid))
print("OK: Giovanni returned %d daily rows, %d with a value" % (len(rows), valid))
'
