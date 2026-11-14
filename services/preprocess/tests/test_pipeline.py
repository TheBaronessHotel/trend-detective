from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import pytest
from google.protobuf import json_format

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from conftest import date_range, raw_dataset, write_imerg_like
from preprocess import errors, pipeline
from preprocess.settings import parse_duration
from preprocess.storage import MemoryStorage


def bundle_task(store: MemoryStorage, delhi_bytes: bytes, dates=("1981-01-01", "2024-12-31")) -> preprocess_pb2.PreprocessTask:
    store.put("raw", "bundle/abc/files/power.json", delhi_bytes, "application/json")
    raw = raw_dataset(
        ingest_pb2.FORMAT_POWER_JSON,
        ["T2M"],
        ["bundle/abc/files/power.json"],
        variable_id="air-temperature-power",
        source=ingest_pb2.SOURCE_POWER,
        point=common_pb2.LatLon(lat=28.61, lon=77.21),
        bbox=common_pb2.BBox(west=77, south=28, east=78, north=29),
        dates=date_range(*dates),
    )
    raw.fetched_at.GetCurrentTime()
    task = preprocess_pb2.PreprocessTask(source_units="°C")
    task.raw.append(raw)
    task.filters.ops.add().qc_range.CopyFrom(preprocess_pb2.QcRange(min=-90, max=60))
    return task


def load(store: MemoryStorage, ref: common_pb2.BlobRef, msg):
    return json_format.Parse(store.get(ref.bucket, ref.key), msg)


def test_bundle_job_writes_a_series_and_reuses_it(delhi_bytes: bytes):
    store = MemoryStorage()
    task = bundle_task(store, delhi_bytes)

    result, cache_hit = pipeline.run(task, store)

    assert not cache_hit
    series = load(store, result.series, data_pb2.Series())
    assert len(series.values) == len(series.times) == len(series.coverage) == 528
    assert series.variable_id == "air-temperature-power"
    assert series.source == ingest_pb2.SOURCE_POWER
    assert series.units == "°C"
    assert series.step == common_pb2.TIME_STEP_MONTHLY
    assert not result.HasField("grid")
    assert store.puts[-1][1].endswith("/result.json")
    csv = store.get("prep", result.series_csv.key).decode().splitlines()
    assert csv[0] == "time,value (°C),coverage,missing_reason"
    assert len(csv) == 529

    # A second run with a new fetched_at is the same work.
    task.raw[0].fetched_at.GetCurrentTime()
    again, cache_hit = pipeline.run(task, store)
    assert cache_hit
    assert again == result


def test_dates_trim_whole_year_files(delhi_bytes: bytes):
    store = MemoryStorage()
    task = bundle_task(store, delhi_bytes, dates=("2000-03-15", "2001-02-28"))
    result, _ = pipeline.run(task, store)
    series = load(store, result.series, data_pb2.Series())
    assert len(series.times) == 12
    assert series.times[0].ToDatetime().strftime("%Y-%m-%d") == "2000-03-01"


def test_grid_job_writes_grid_and_weighted_series(tmp_path: Path):
    store = MemoryStorage()
    lats = np.array([0.5, 1.5, 2.5])
    lons = np.array([10.5, 11.5])
    keys = []
    for month in ("2020-01-01", "2020-02-01"):
        path = tmp_path / f"{month}.nc4"
        # In January 5 of 6 pixels are fill, below the 0.5 coverage default.
        fill = (0, slice(None), slice(0, 3)) if month.endswith("01-01") else None
        write_imerg_like(path, [month], lats, lons)
        if fill is not None:
            import xarray as xr

            with xr.open_dataset(path, group="Grid") as ds:
                data = ds.load()
            data["precipitation"].values[0, :, :] = np.nan
            data["precipitation"].values[0, 0, 0] = 1.0
            data["precipitation"].encoding["_FillValue"] = np.float32(-9999.9)
            path.unlink()
            data.to_netcdf(path, group="Grid", engine="netcdf4")
        store.put("raw", f"harmony/k/files/{path.name}", path.read_bytes(), "application/x-netcdf")
        keys.append(f"harmony/k/files/{path.name}")

    raw = raw_dataset(
        ingest_pb2.FORMAT_NETCDF4,
        ["Grid/precipitation"],
        keys,
        variable_id="precipitation-gpm",
        source=ingest_pb2.SOURCE_HARMONY_SUBSET,
        bbox=common_pb2.BBox(west=10, south=0, east=12, north=3),
        dates=date_range("2020-01-01", "2020-02-29"),
    )
    task = preprocess_pb2.PreprocessTask(source_units="mm/hr")
    task.raw.append(raw)

    result, _ = pipeline.run(task, store)

    grid = load(store, result.grid, data_pb2.Grid())
    assert (grid.rows, grid.cols) == (3, 2)
    assert list(grid.lats) == [2.5, 1.5, 0.5]
    values = np.frombuffer(store.get("prep", grid.values.key), "<f4").reshape(2, 3, 2)
    assert np.isnan(values[0]).sum() == 5

    series = load(store, result.series, data_pb2.Series())
    assert np.isnan(series.values[0])
    assert series.coverage[0] == pytest.approx(1 / 6)
    assert series.missing[0].reason == data_pb2.MISSING_REASON_LOW_COVERAGE
    # February has every pixel, so its value is the cos(lat)-weighted mean.
    feb = values[1]
    w = np.cos(np.deg2rad(np.array(grid.lats)))[:, None] * np.ones_like(feb)
    assert series.values[1] == pytest.approx(float((feb * w).sum() / w.sum()), rel=1e-6)


def test_max_grid_block_averages(tmp_path: Path):
    store = MemoryStorage()
    path = tmp_path / "a.nc4"
    write_imerg_like(path, ["2020-01-01"], np.arange(8) + 0.5, np.arange(8) + 0.5)
    store.put("raw", "h/a.nc4", path.read_bytes(), "application/x-netcdf")
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["Grid/precipitation"], ["h/a.nc4"])
    task = preprocess_pb2.PreprocessTask()
    task.raw.append(raw)
    task.aggregation.max_grid = 3

    result, _ = pipeline.run(task, store)

    grid = load(store, result.grid, data_pb2.Grid())
    assert (grid.rows, grid.cols) == (3, 3)


def test_all_missing_is_no_data(tmp_path: Path):
    store = MemoryStorage()
    path = tmp_path / "power.json"
    path.write_text(json.dumps({"properties": {"parameter": {"T2M": {"202001": -999.0}}}, "header": {"fill_value": -999}}))
    store.put("raw", "p/power.json", path.read_bytes(), "application/json")
    task = preprocess_pb2.PreprocessTask()
    task.raw.append(raw_dataset(ingest_pb2.FORMAT_POWER_JSON, ["T2M"], ["p/power.json"]))

    with pytest.raises(errors.TaskFailure) as info:
        pipeline.run(task, store)
    assert info.value.code == errors.task_pb2.ERROR_CODE_NO_DATA


def test_unknown_format_is_unsupported():
    store = MemoryStorage()
    store.put("raw", "a/x.tif", b"", "image/tiff")
    task = preprocess_pb2.PreprocessTask()
    task.raw.append(raw_dataset(ingest_pb2.FORMAT_GEOTIFF, ["NDVI"], ["a/x.tif"]))
    with pytest.raises(errors.TaskFailure) as info:
        pipeline.run(task, store)
    assert info.value.code == errors.task_pb2.ERROR_CODE_UNSUPPORTED


@pytest.mark.parametrize(("text", "seconds"), [("2m", 120), ("90s", 90), ("1m30s", 90), ("500ms", 0.5), ("1h", 3600)])
def test_parse_duration(text, seconds):
    assert parse_duration(text) == seconds


@pytest.mark.parametrize("text", ["", "2", "2x", "m2"])
def test_parse_duration_rejects(text):
    with pytest.raises(ValueError):
        parse_duration(text)
