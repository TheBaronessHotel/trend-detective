from __future__ import annotations

import pytest
from google.protobuf import json_format

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.gateway.v1 import task_pb2

from conftest import catalog_plan, delhi
from analysis import pipeline
from analysis.errors import TaskFailure
from analysis.storage import MemoryStorage

PREP_KEY = "0f3c/series.json"


def bundle_task(store: MemoryStorage, series=None) -> analysis_pb2.AnalysisTask:
    data = json_format.MessageToJson(series or delhi("T2M"), indent=None).encode()
    ref = store.put("prep", PREP_KEY, data, "application/json")
    task = analysis_pb2.AnalysisTask(tests=catalog_plan())
    task.input.series.CopyFrom(ref)
    return task


def test_writes_the_series_analysis_and_reuses_it():
    store = MemoryStorage()
    task = bundle_task(store)

    result, cache_hit = pipeline.run(task, store)

    assert not cache_hit
    assert result.series.bucket == "analysis"
    assert not result.HasField("grid")
    doc = json_format.Parse(store.get("analysis", result.series.key), results_pb2.SeriesAnalysis())
    assert doc.n_valid == 528
    assert doc.HasField("regression") and doc.HasField("mann_kendall")
    assert store.puts[-1] == ("analysis", f"{pipeline.cache_key(task)}/result.json")

    again, cache_hit = pipeline.run(task, store)
    assert cache_hit
    assert again == result


def test_a_different_plan_is_different_work():
    store = MemoryStorage()
    task = bundle_task(store)
    other = analysis_pb2.AnalysisTask()
    other.CopyFrom(task)
    other.tests.alpha = 0.01
    assert pipeline.cache_key(task) != pipeline.cache_key(other)


def test_nan_survives_the_round_trip():
    # protojson writes NaN as the string "NaN". Missing values must come back
    # as NaN, not break the parser.
    series = delhi("T2M")
    series.values[5] = float("nan")
    store = MemoryStorage()
    result, _ = pipeline.run(bundle_task(store, series), store)
    doc = json_format.Parse(store.get("analysis", result.series.key), results_pb2.SeriesAnalysis())
    assert doc.n_valid == 527


def test_a_task_without_a_series_is_refused():
    with pytest.raises(TaskFailure) as info:
        pipeline.run(analysis_pb2.AnalysisTask(tests=catalog_plan()), MemoryStorage())
    assert info.value.code == task_pb2.ERROR_CODE_INVALID_REQUEST


def test_an_unreadable_series_is_our_bug():
    store = MemoryStorage()
    task = bundle_task(store)
    store.put("prep", PREP_KEY, b"{not json", "application/json")
    with pytest.raises(TaskFailure) as info:
        pipeline.run(task, store)
    assert info.value.code == task_pb2.ERROR_CODE_INTERNAL
    assert not info.value.transient


def test_a_missing_series_is_retried():
    store = MemoryStorage()
    task = bundle_task(store)
    del store.objects[("prep", PREP_KEY)]
    with pytest.raises(TaskFailure) as info:
        pipeline.run(task, store)
    assert info.value.transient
