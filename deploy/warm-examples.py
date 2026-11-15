"""Run every "Try an example" job once, so NASA's data is cached.

Usage: python3 deploy/warm-examples.py [service URL]

A fresh stack has an empty cache, so the first click on an example waits on
NASA for minutes, and AppEEARS can queue for longer. Run this after `make up`
and before a demo: it starts each example from services/web/src/lib/
examples.json with the same dates the app sends, then waits for them all.
Only the standard library, so it runs wherever Python 3.8 does.
"""

import calendar
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

API = "http://127.0.0.1:8080/trenddetective.gateway.v1.GatewayService"
EXAMPLES = Path(__file__).resolve().parent.parent / "services/web/src/lib/examples.json"
FINISHED = {"JOB_STATE_SUCCEEDED", "JOB_STATE_FAILED", "JOB_STATE_CANCELED"}
POLL_SECONDS = 15
# AppEEARS may queue for an hour or two; the gateway stops those itself.
GIVE_UP_SECONDS = 3 * 60 * 60


def call(base, method, body):
    req = urllib.request.Request(
        f"{base}/{method}", data=json.dumps(body).encode(), headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        return json.load(resp)


def dates(start, end):
    """A month range as the app sends it: the 1st of the first month to the last day of the last."""
    sy, sm = map(int, start.split("-"))
    ey, em = map(int, end.split("-"))
    last = calendar.monthrange(ey, em)[1]
    return {"start": {"year": sy, "month": sm, "day": 1}, "end": {"year": ey, "month": em, "day": last}}


def problem(job):
    for t in job.get("tasks", []):
        err = t.get("error")
        if err:
            return err.get("userMessage") or err.get("code", "")
    return job.get("state", "")


def main():
    base = sys.argv[1] if len(sys.argv) > 1 else API
    examples = json.loads(EXAMPLES.read_text(encoding="utf-8"))
    jobs = {}
    for e in examples:
        spec = {"variableId": e["variableId"], "bbox": e["box"], "dates": dates(e["start"], e["end"])}
        try:
            jobs[e["id"]] = call(base, "CreateJob", {"spec": spec})["job"]["id"]
        except urllib.error.HTTPError as err:
            print(f"FAIL {e['id']}: the gateway refused it: {err.read().decode(errors='replace')}")
        except urllib.error.URLError as err:
            print(f"FAIL: couldn't reach the gateway at {base}. Is the stack up (make up)? {err.reason}")
            return 1
    print(f"Started {len(jobs)} of {len(examples)} examples. New ones wait on NASA, sometimes for many minutes.")

    started = time.monotonic()
    done = {}
    while len(done) < len(jobs) and time.monotonic() - started < GIVE_UP_SECONDS:
        for name, job_id in jobs.items():
            if name in done:
                continue
            try:
                job = call(base, "GetJob", {"jobId": job_id})["job"]
            except (urllib.error.URLError, TimeoutError):
                continue  # A blip; ask again next round.
            if job.get("state") in FINISHED:
                done[name] = job
                minutes = (time.monotonic() - started) / 60
                ok = job["state"] == "JOB_STATE_SUCCEEDED"
                print(f"{'OK  ' if ok else 'FAIL'} {name} after {minutes:.1f} min{'' if ok else ': ' + problem(job)}")
        if len(done) < len(jobs):
            time.sleep(POLL_SECONDS)

    waiting = [name for name in jobs if name not in done]
    for name in waiting:
        print(f"FAIL {name}: still running after {GIVE_UP_SECONDS // 3600} hours")
    failed = [n for n, j in done.items() if j["state"] != "JOB_STATE_SUCCEEDED"] + waiting
    if failed or len(jobs) < len(examples):
        return 1
    print("All examples are cached. Each now finishes in seconds in the app.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
