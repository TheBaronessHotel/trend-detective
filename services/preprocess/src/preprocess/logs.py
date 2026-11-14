"""Log lines in the same text format and field names as the Go services.

Each record is one line of key=value pairs, like slog's text handler:

    time=2026-11-14T16:24:59.559Z level=INFO msg=prepared job_id=... steps=528

Inside a task every record also has job_id and task_id, and trace_id when the
task carried a traceparent header, so one job can be followed across Go and
Python logs.
"""

from __future__ import annotations

import logging
import sys
from datetime import UTC, datetime

_LEVELS = {"trace": logging.DEBUG, "debug": logging.DEBUG, "info": logging.INFO, "warn": logging.WARNING, "error": logging.ERROR}


def _value(v: object) -> str:
    text = ("true" if v else "false") if isinstance(v, bool) else str(v)
    if text == "" or any(c in text for c in ' "=\\\n\t'):
        return '"' + text.replace("\\", "\\\\").replace('"', '\\"').replace("\n", "\\n") + '"'
    return text


class TextFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        pairs = [
            ("time", datetime.fromtimestamp(record.created, UTC).isoformat(timespec="milliseconds").replace("+00:00", "Z")),
            ("level", record.levelname.replace("WARNING", "WARN")),
            ("msg", record.getMessage()),
        ]
        pairs.extend((getattr(record, "fields", {}) or {}).items())
        if record.exc_info:
            pairs.append(("trace", self.formatException(record.exc_info)))
        return " ".join(f"{k}={_value(v)}" for k, v in pairs)


def setup(level: str) -> None:
    name = level.strip().lower() or "info"
    if name not in _LEVELS:
        raise ValueError(f"LOG_LEVEL must be one of {', '.join(_LEVELS)}, got {level!r}")
    handler = logging.StreamHandler(sys.stdout)
    handler.setFormatter(TextFormatter())
    root = logging.getLogger()
    root.handlers[:] = [handler]
    root.setLevel(_LEVELS[name])
    # Keep library chatter out unless asked for.
    for noisy in ("botocore", "boto3", "urllib3", "nats"):
        logging.getLogger(noisy).setLevel(max(_LEVELS[name], logging.WARNING))


class TaskLog(logging.LoggerAdapter):
    """Adds the task's fields to every record, merged with the call's own."""

    def process(self, msg, kwargs):
        extra = kwargs.setdefault("extra", {})
        fields = dict(self.extra or {})
        fields.update(extra.get("fields", {}))
        extra["fields"] = fields
        return msg, kwargs
