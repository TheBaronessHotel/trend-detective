"""One analysis task, from a prepared series to documents in the analysis
bucket.

This module knows nothing about NATS. The worker hands it an AnalysisTask and
reports whatever it returns or raises.
"""

from __future__ import annotations

import hashlib
import logging

from google.protobuf import json_format

from trenddetective.analysis.v1 import analysis_pb2
from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2

from . import errors, suite
from .storage import Storage

ANALYSIS_BUCKET = "analysis"

# Changes whenever the documents this code writes could change for the same
# task, like a new test or a fixed formula. It is part of the cache key, so
# bumping it makes every cached result stale at once.
CODE_VERSION = "1"
KEY_VERSION = "analysis/v1"

RESULT_FILE = "result.json"

log = logging.getLogger("analysis.pipeline")


def cache_key(task: analysis_pb2.AnalysisTask) -> str:
    """Identifies what a task would write. Two tasks with the same key write the
    same documents, so the second one reuses the first one's.

    The input's keys already change when the data does, because preprocess
    keys its documents by a hash of its own inputs.
    """
    h = hashlib.sha256()
    h.update(f"{KEY_VERSION}\n{CODE_VERSION}\n".encode())
    h.update(task.SerializeToString(deterministic=True))
    return h.hexdigest()


def run(
    task: analysis_pb2.AnalysisTask, store: Storage, logger: logging.LoggerAdapter | logging.Logger = log
) -> tuple[analysis_pb2.AnalysisResult, bool]:
    """Runs one task. Returns the result and whether it came from the cache."""
    if not task.input.HasField("series"):
        raise errors.invalid("The job has no prepared series to analyze.", "AnalysisTask.input.series is unset")

    key = cache_key(task)
    result_key = f"{key}/{RESULT_FILE}"
    if store.exists(ANALYSIS_BUCKET, result_key):
        cached = json_format.Parse(store.get(ANALYSIS_BUCKET, result_key), analysis_pb2.AnalysisResult())
        return cached, True

    series = load_series(store, task.input.series)
    analysis = suite.run(series, task.tests, logger)

    result = analysis_pb2.AnalysisResult()
    result.series.CopyFrom(_put_json(store, f"{key}/series.json", analysis))
    # Written last, so its presence means every other file is in.
    _put_json(store, result_key, result)

    fields = {"n_valid": analysis.n_valid, "n_total": analysis.n_total, "warnings": len(analysis.warnings)}
    if analysis.HasField("regression"):
        fields["slope_per_decade"] = f"{analysis.regression.slope_per_decade:.4g}"
        fields["p_regression"] = f"{analysis.regression.p_value:.3g}"
    if analysis.HasField("mann_kendall"):
        fields["p_mann_kendall"] = f"{analysis.mann_kendall.p_value:.3g}"
    logger.info("analyzed", extra={"fields": fields})
    return result, False


def load_series(store: Storage, ref: common_pb2.BlobRef) -> data_pb2.Series:
    data = store.get(ref.bucket, ref.key)
    try:
        return json_format.Parse(data, data_pb2.Series(), ignore_unknown_fields=True)
    except json_format.ParseError as err:
        raise errors.internal(
            "The prepared data couldn't be read.", f"{ref.bucket}/{ref.key} isn't a Series: {err}"
        ) from err


def _put_json(store: Storage, key: str, msg) -> common_pb2.BlobRef:
    data = json_format.MessageToJson(msg, indent=None).encode("utf-8")
    return store.put(ANALYSIS_BUCKET, key, data, "application/json")
