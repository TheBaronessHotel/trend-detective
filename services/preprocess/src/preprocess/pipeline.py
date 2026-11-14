"""One preprocess task, from raw files to documents in the prep bucket.

This module knows nothing about NATS. The worker hands it a PreprocessTask and
reports whatever it returns or raises.
"""

from __future__ import annotations

import hashlib
import logging
import tempfile
from pathlib import Path, PurePosixPath

from google.protobuf import json_format

from trenddetective.preprocess.v1 import preprocess_pb2

from . import canonical, decoders, errors, output
from .canonical import Decoded
from .storage import Storage

PREP_BUCKET = "prep"

# Changes whenever the documents this code writes could change for the same
# task, like a new decoder rule or the filter chain arriving. It is part of the
# cache key, so bumping it makes every cached result stale at once.
CODE_VERSION = "1"
KEY_VERSION = "preprocess/v1"

RESULT_FILE = "result.json"

log = logging.getLogger("preprocess.pipeline")


def cache_key(task: preprocess_pb2.PreprocessTask) -> str:
    """Identifies what a task would write. Two tasks with the same key write the
    same documents, so the second one reuses the first one's.

    fetched_at is left out: it records when ingest ran, not what it fetched,
    and the file keys already change when the data does.
    """
    t = preprocess_pb2.PreprocessTask()
    t.CopyFrom(task)
    for raw in t.raw:
        raw.ClearField("fetched_at")
    h = hashlib.sha256()
    h.update(f"{KEY_VERSION}\n{CODE_VERSION}\n".encode())
    h.update(t.SerializeToString(deterministic=True))
    return h.hexdigest()


def run(
    task: preprocess_pb2.PreprocessTask, store: Storage, logger: logging.LoggerAdapter | logging.Logger = log
) -> tuple[preprocess_pb2.PreprocessResult, bool]:
    """Runs one task. Returns the result and whether it came from the cache."""
    if not task.raw:
        raise errors.invalid("The job has no data to prepare.", "PreprocessTask.raw is empty")

    key = cache_key(task)
    result_key = f"{key}/{RESULT_FILE}"
    if store.exists(PREP_BUCKET, result_key):
        cached = json_format.Parse(store.get(PREP_BUCKET, result_key), preprocess_pb2.PreprocessResult())
        return cached, True

    dec = decode_all(task, store)
    for note in dec.notes:
        logger.warning(note)
    if task.filters.ops:
        logger.info("filter chain not applied yet (PRE-2); writing decoded values")
    if not output.has_values(dec):
        raise errors.no_data(
            "NASA has no valid data for this rectangle and these dates.", "every value is missing"
        )

    raw = task.raw[0]
    result = preprocess_pb2.PreprocessResult()

    if dec.gridded:
        g = output.grid(dec, raw.variable_id, task.aggregation)
        g.grid.values.CopyFrom(store.put(PREP_BUCKET, f"{key}/grid-values.f32", g.values, "application/octet-stream"))
        result.grid.CopyFrom(_put_json(store, f"{key}/grid.json", g.grid))

    series = output.region_series(dec, raw, raw.variable_id, task.aggregation)
    result.series.CopyFrom(_put_json(store, f"{key}/series.json", series))
    result.series_csv.CopyFrom(store.put(PREP_BUCKET, f"{key}/series.csv", output.series_csv(series), "text/csv"))

    # Written last, so its presence means every other file is in.
    _put_json(store, result_key, result)
    logger.info(
        "prepared",
        extra={"fields": {"steps": len(series.times), "gridded": dec.gridded, "units": dec.units}},
    )
    return result, False


def decode_all(task: preprocess_pb2.PreprocessTask, store: Storage) -> Decoded:
    """Decodes every RawDataset of the task and joins them along time."""
    pieces: list[Decoded] = []
    with tempfile.TemporaryDirectory(prefix="preprocess-") as tmp:
        for n, raw in enumerate(task.raw):
            decoder = decoders.for_format(raw.format)
            paths: list[Path] = []
            for i, f in enumerate(raw.files):
                name = PurePosixPath(f.blob.key).name or f"file-{i}"
                dest = Path(tmp) / f"{n:03d}-{i:05d}-{name}"
                store.download(f.blob.bucket, f.blob.key, dest)
                paths.append(dest)
            if not paths:
                raise errors.no_data("No files arrived for this request.", f"raw[{n}] has no files")
            dec = decoder(paths, raw, task.source_units)
            data = canonical.trim(dec.data, raw.dates, dec.step)
            data = canonical.clip_bbox(data, raw.bbox if raw.HasField("bbox") else None)
            dec.data = data
            pieces.append(dec)

    first = pieces[0]
    if len(pieces) == 1:
        return first
    data = canonical.combine([p.data for p in pieces])
    notes = [note for p in pieces for note in p.notes]
    return Decoded(data=data, units=first.units, step=first.step, point=first.point, notes=notes)


def _put_json(store: Storage, key: str, msg):
    data = json_format.MessageToJson(msg, indent=None).encode("utf-8")
    return store.put(PREP_BUCKET, key, data, "application/json")
