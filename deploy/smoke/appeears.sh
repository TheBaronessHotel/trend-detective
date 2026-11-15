#!/bin/sh
# Live check of the appeears adapter against NASA AppEEARS, run by
# `make smoke-appeears`. It needs EARTHDATA_USERNAME and EARTHDATA_PASSWORD in
# .env, so CI skips it. AppEEARS takes requests in turn, so this can take 5 to
# 15 minutes when it's busy.
#
# It asks for two years of monthly NDVI over a small box near Delhi, stopping
# after preprocess, and checks the series has 24 months of plausible values.
set -eu

api=http://127.0.0.1:8080/trenddetective.gateway.v1.GatewayService
call() { curl -s -H 'Content-Type: application/json' -d "$2" "$api/$1"; }

spec='{"spec": {"variableId": "vegetation-modis",
  "bbox": {"west": 77.0, "south": 28.4, "east": 77.4, "north": 28.8},
  "dates": {"start": {"year": 2019, "month": 1, "day": 1}, "end": {"year": 2020, "month": 12, "day": 31}},
  "stopAfter": "STAGE_PREPROCESS"}}'
id=$(call CreateJob "$spec" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["job"]["id"])')
echo "Job $id: asking NASA AppEEARS for MODIS NDVI"
python3 -I deploy/smoke/watch.py "$id" "$api" || { echo "FAIL: job didn't succeed"; call GetJob "{\"jobId\": \"$id\"}"; exit 1; }

url=$(call GetResultURL "{\"jobId\": \"$id\", \"artifact\": \"ARTIFACT_SERIES\"}" | python3 -I -c 'import json,sys; print(json.load(sys.stdin)["url"])')
curl -s "$url" | python3 -I -c '
import json, sys
s = json.load(sys.stdin)
values = [v for v in s["values"] if v != "NaN"]
ok = len(s["values"]) == 24 and len(values) >= 20 and all(-0.2 <= v <= 1 for v in values) and s.get("units") == "NDVI"
print("OK:" if ok else "FAIL:", len(s["values"]), "months of NDVI from", min(values), "to", max(values))
sys.exit(0 if ok else 1)
' || { echo "FAIL: unexpected series"; exit 1; }
