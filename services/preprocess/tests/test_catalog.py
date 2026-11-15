"""Runs every filter chain in the gateway's catalog.yaml.

The gateway copies a variable's chain into each preprocess task, so a catalog
change that preprocess can't carry out would only fail when a user picks that
variable. This test fails in CI instead, in seconds.
"""

from __future__ import annotations

import numpy as np
import pandas as pd
import pytest
import xarray as xr
import yaml
from google.protobuf import json_format

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from conftest import REPO, date_range, raw_dataset
from preprocess import canonical, filters, output, pipeline
from preprocess.canonical import Decoded
from preprocess.storage import MemoryStorage

CATALOG = yaml.safe_load((REPO / "services" / "gateway" / "catalog.yaml").read_text(encoding="utf-8"))
ENTRIES = {e["id"]: e for e in CATALOG["variables"]}


def chain_of(entry: dict) -> preprocess_pb2.FilterChain:
    return json_format.ParseDict(entry.get("filters") or {}, preprocess_pb2.FilterChain())


def synthetic(entry: dict, daily: bool, years: int = 6) -> Decoded:
    """Data that passes the entry's qc_range, with a seasonal cycle, a trend
    and a few gaps, at the step the source delivers."""
    ops = chain_of(entry).ops
    qc = next((op.qc_range for op in ops if op.HasField("qc_range")), None)
    lo, hi = (qc.min, qc.max) if qc else (1.0, 100.0)
    centre, amp = lo + (hi - lo) * 0.5, (hi - lo) * 0.05
    freq = "D" if daily else "MS"
    times = pd.date_range("2005-01-01", f"{2005 + years - 1}-12-31", freq=freq)
    phase = 2 * np.pi * (times.day_of_year.to_numpy() / 365.25)
    v = centre + amp * np.sin(phase) + amp * 0.2 * np.arange(len(times)) / len(times)
    v[::17] = np.nan
    shape = (len(times), 2, 2) if entry.get("grid") else (len(times),)
    if entry.get("grid"):
        v = np.repeat(v[:, None, None], 4, axis=1).reshape(shape)
        da = xr.DataArray(v, dims=("time", "lat", "lon"), coords={"time": times.values, "lat": [1.0, 0.0], "lon": [0.0, 1.0]})
    else:
        da = xr.DataArray(v, dims=("time",), coords={"time": times.values})
    step = common_pb2.TIME_STEP_DAILY if daily else common_pb2.TIME_STEP_MONTHLY
    column = next((op.column_average for op in ops if op.HasField("column_average")), None)
    if column is None:
        return Decoded(data=canonical.make_dataset(da), units=entry.get("source_units", ""), step=step)
    # A profile on three pressure levels, like AIRS methane, with the surface
    # pressure helper the step names.
    levels = xr.DataArray([1000.0, 500.0, 100.0], dims="level")
    profile = (da * xr.ones_like(levels)).assign_coords(level=levels.values).transpose("time", "level", ...)
    ds = canonical.make_dataset(profile)
    ds[canonical.helper_name(column.surface_pressure)] = xr.full_like(da, 1000.0)
    return Decoded(data=ds, units=entry.get("source_units", ""), step=step)


def cases():
    for vid, entry in ENTRIES.items():
        names = [op.WhichOneof("op") for op in chain_of(entry).ops]
        daily = "monthly_mean" in names
        yield pytest.param(vid, daily, id=f"{vid}-{'daily' if daily else 'monthly'}")
        if vid == "user-csv":
            # A user's CSV can be daily; preprocess adds the monthly mean.
            yield pytest.param(vid, True, id="user-csv-daily")


@pytest.mark.parametrize(("vid", "daily"), list(cases()))
def test_catalog_chain_runs(vid, daily):
    entry = ENTRIES[vid]
    dec = filters.apply(synthetic(entry, daily), chain_of(entry))
    raw = ingest_pb2.RawDataset(variable_id=vid)
    series = output.region_series(dec, raw, vid, preprocess_pb2.Aggregation())

    assert dec.step == common_pb2.TIME_STEP_MONTHLY
    assert len(series.times) == 6 * 12
    # The units the catalog shows are the units the chain produces.
    if entry.get("units") and entry["units"] != "your units":
        assert series.units == entry["units"]
    names = [op.WhichOneof("op") for op in chain_of(entry).ops]
    if "anomaly" in names:
        anomalies = np.array(series.anomalies)
        assert len(anomalies) == len(series.times)
        assert np.isfinite(anomalies).sum() > len(anomalies) // 2
        assert len(series.normals) == 12
    if "gap_fill_display" in names:
        assert len(series.display_values) == len(series.times)


def test_delhi_bundle_through_its_catalog_chain(delhi_bytes):
    entry = ENTRIES["air-temperature-power"]
    store = MemoryStorage()
    store.put("raw", "b/power.json", delhi_bytes, "application/json")
    raw = raw_dataset(
        ingest_pb2.FORMAT_POWER_JSON,
        ["T2M"],
        ["b/power.json"],
        variable_id="air-temperature-power",
        source=ingest_pb2.SOURCE_POWER,
        dates=date_range("1981-01-01", "2024-12-31"),
    )
    task = preprocess_pb2.PreprocessTask(source_units=entry["source_units"], filters=chain_of(entry))
    task.raw.append(raw)

    result, _ = pipeline.run(task, store)

    series = json_format.Parse(store.get("prep", result.series.key), data_pb2.Series())
    a = np.array(series.anomalies).reshape(44, 12)
    # Over the whole-record baseline, each calendar month's anomalies average
    # zero, up to the trend's rise within the year, a few thousandths of a
    # degree for Delhi.
    assert np.abs(np.nanmean(a, axis=0)).max() < 0.01
    assert (series.baseline.start_year, series.baseline.end_year) == (1981, 2024)
    assert all(n.usable and n.years == 44 for n in series.normals)
    # Delhi: May and June are the hottest months, January the coolest.
    means = [n.mean for n in series.normals]
    assert max(means) in (means[4], means[5]) and min(means) == means[0]
    assert len(series.display_values) == 528
