"""Settings from environment variables, with the same names and formats the
Go services use."""

from __future__ import annotations

import os
import re
from dataclasses import dataclass

_DURATION = re.compile(r"(\d+(?:\.\d+)?)(ms|s|m|h)")
_UNIT_SECONDS = {"ms": 0.001, "s": 1.0, "m": 60.0, "h": 3600.0}


def parse_duration(text: str) -> float:
    """Reads a Go-style duration like "2m", "90s" or "1m30s" into seconds."""
    text = text.strip()
    pos, total = 0, 0.0
    for m in _DURATION.finditer(text):
        if m.start() != pos:
            break
        total += float(m.group(1)) * _UNIT_SECONDS[m.group(2)]
        pos = m.end()
    if pos != len(text) or not text:
        raise ValueError(f"not a duration like 2m or 90s: {text!r}")
    return total


@dataclass(frozen=True)
class Settings:
    nats_url: str
    s3_endpoint: str
    s3_access_key: str
    s3_secret_key: str
    concurrency: int = 2
    ack_wait: float = 120.0
    log_level: str = "info"

    @classmethod
    def from_env(cls) -> Settings:
        required = ["NATS_URL", "S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY"]
        missing = [name for name in required if not os.environ.get(name)]
        if missing:
            raise ValueError(f"missing required settings: {', '.join(missing)}")
        concurrency = int(os.environ.get("PREPROCESS_CONCURRENCY", "2"))
        if concurrency < 1:
            raise ValueError("PREPROCESS_CONCURRENCY must be at least 1")
        return cls(
            nats_url=os.environ["NATS_URL"],
            s3_endpoint=os.environ["S3_ENDPOINT"],
            s3_access_key=os.environ["S3_ACCESS_KEY"],
            s3_secret_key=os.environ["S3_SECRET_KEY"],
            concurrency=concurrency,
            ack_wait=parse_duration(os.environ.get("PREPROCESS_ACK_WAIT", "2m")),
            log_level=os.environ.get("LOG_LEVEL", "info"),
        )
