"""Watch a job through the gateway's WatchJob stream.

Usage: python3 watch.py <job id> <service URL>

Speaks the Connect streaming protocol over plain HTTP with only the standard
library. Every message is a 5-byte header (flags, then length) and a JSON
body. Prints the states it saw, and exits 0 when the stream ends with the job
succeeded.
"""

import json
import struct
import sys
import urllib.request

END_STREAM = 0x02


def frames(resp):
    while True:
        head = resp.read(5)
        if len(head) < 5:
            return
        flags, size = struct.unpack(">BI", head)
        yield flags, resp.read(size)


def main():
    job_id, base = sys.argv[1], sys.argv[2]
    body = json.dumps({"jobId": job_id}).encode()
    req = urllib.request.Request(
        base + "/WatchJob",
        data=struct.pack(">BI", 0, len(body)) + body,
        headers={"Content-Type": "application/connect+json", "Connect-Protocol-Version": "1"},
    )
    states = []
    # A job can go minutes without a change, for example while Harmony works.
    with urllib.request.urlopen(req, timeout=600) as resp:
        for flags, payload in frames(resp):
            message = json.loads(payload or b"{}")
            if flags & END_STREAM:
                if message.get("error"):
                    print("stream ended with an error:", message["error"])
                    return 1
                break
            states.append(message["job"]["state"])
    print("WatchJob saw:", " -> ".join(states) or "nothing")
    return 0 if states and states[-1] == "JOB_STATE_SUCCEEDED" else 1


if __name__ == "__main__":
    sys.exit(main())
