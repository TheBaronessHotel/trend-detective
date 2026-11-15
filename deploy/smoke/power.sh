#!/bin/sh
# Live check of the power adapter against NASA POWER, run by
# `make smoke-power`. It needs no token, but CI skips it so CI doesn't depend
# on NASA's servers.
#
# It asks for ten years of air temperature over a small box near Lisbon,
# stopping after ingest. POWER should sample the middle of the box.
set -eu
. "$(dirname "$0")/python.sh"

api=http://127.0.0.1:8080/trenddetective.gateway.v1.GatewayService
call() { curl -s -H 'Content-Type: application/json' -d "$2" "$api/$1"; }

spec='{"spec": {"variableId": "air-temperature-power",
  "bbox": {"west": -9.5, "south": 38.5, "east": -9.0, "north": 39.0},
  "dates": {"start": {"year": 2001, "month": 1, "day": 1}, "end": {"year": 2010, "month": 12, "day": 31}},
  "stopAfter": "STAGE_INGEST"}}'
id=$(call CreateJob "$spec" | "$PY" -I -c 'import json,sys; print(json.load(sys.stdin)["job"]["id"])')
echo "Job $id: fetching from NASA POWER"
"$PY" -I deploy/smoke/watch.py "$id" "$api" || { echo "FAIL: job didn't succeed"; call GetJob "{\"jobId\": \"$id\"}"; exit 1; }

call GetJob "{\"jobId\": \"$id\"}" | "$PY" -I -c '
import json, sys
raw = json.load(sys.stdin)["job"]["results"]["raw"][0]
point, files = raw["point"], raw["files"]
ok = raw["source"] == "SOURCE_POWER" and raw["variables"] == ["T2M"] and len(files) == 1 \
    and abs(point["lat"] - 38.75) < 1e-9 and abs(point["lon"] + 9.25) < 1e-9
print("OK: POWER sampled", point, "into", files[0]["blob"]["key"].split("/")[-1], files[0]["blob"]["sizeBytes"], "bytes" if ok else "")
sys.exit(0 if ok else 1)
' || { echo "FAIL: unexpected raw dataset"; exit 1; }
