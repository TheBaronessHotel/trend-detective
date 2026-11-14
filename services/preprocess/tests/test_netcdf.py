from __future__ import annotations

from pathlib import Path

import numpy as np
import pandas as pd
import pytest
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2

from conftest import raw_dataset, write_imerg_like
from preprocess import canonical, errors
from preprocess.decoders import netcdf

LATS = np.array([10.05, 10.15, 10.25])  # south first, like IMERG
LONS = np.array([20.05, 20.15])


def test_imerg_layout_becomes_canonical(tmp_path: Path):
    a, b = tmp_path / "a.nc4", tmp_path / "b.nc4"
    # Ingest lists files in download order, which needn't be time order.
    write_imerg_like(b, ["2020-02-01"], LATS, LONS)
    write_imerg_like(a, ["2020-01-01"], LATS, LONS, fill_at=(0, 1, 2))
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["Grid/precipitation"], ["b.nc4", "a.nc4"])

    dec = netcdf.decode([b, a], raw, "mm/hr")

    assert dec.gridded
    assert dec.values.dims == ("time", "lat", "lon")
    assert list(dec.data["lat"].values) == sorted(LATS, reverse=True)
    assert list(dec.data["lon"].values) == list(LONS)
    assert [str(t)[:10] for t in dec.data["time"].values] == ["2020-01-01", "2020-02-01"]
    # The fill was at lon index 1, lat index 2 (north-most), so after the flip
    # it sits at lat 0, lon 1.
    assert np.isnan(dec.values.values[0, 0, 1])
    assert dec.reasons.values[0, 0, 1] == data_pb2.MISSING_REASON_FILL_VALUE
    assert np.isnan(dec.values.values).sum() == 1
    assert dec.step == common_pb2.TIME_STEP_MONTHLY
    assert dec.units == "mm/hr"


def test_merra_layout_with_0_360_longitudes_and_half_hour_stamps(tmp_path: Path):
    times = pd.to_datetime(["2020-01-01T00:30", "2020-02-01T00:30", "2020-03-01T00:30"]).values
    lons = np.array([350.0, 355.0, 0.0, 5.0])
    lats = np.array([0.0, 0.5])
    da = xr.DataArray(
        np.full((3, 2, 4), 280.0, dtype="float32"),
        dims=("time", "lat", "lon"),
        coords={"time": times, "lat": lats, "lon": lons},
        attrs={"units": "K"},
        name="T2M",
    )
    path = tmp_path / "merra.nc4"
    da.to_dataset().to_netcdf(path, engine="netcdf4")
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["T2M"], ["merra.nc4"])

    dec = netcdf.decode([path], raw, "K")

    assert list(dec.data["lon"].values) == [-10.0, -5.0, 0.0, 5.0]
    assert [str(t)[:16] for t in dec.data["time"].values] == ["2020-01-01T00:00", "2020-02-01T00:00", "2020-03-01T00:00"]


def test_units_mismatch_is_noted_and_task_units_win(tmp_path: Path):
    path = tmp_path / "a.nc4"
    write_imerg_like(path, ["2020-01-01"], LATS, LONS)
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["Grid/precipitation"], ["a.nc4"])
    dec = netcdf.decode([path], raw, "mm/day")
    assert dec.units == "mm/day"
    assert len(dec.notes) == 1


def test_missing_variable_is_an_invalid_request(tmp_path: Path):
    path = tmp_path / "a.nc4"
    write_imerg_like(path, ["2020-01-01"], LATS, LONS)
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["Grid/nope"], ["a.nc4"])
    with pytest.raises(errors.TaskFailure) as info:
        netcdf.decode([path], raw, "")
    assert "nope" in info.value.detail


def test_clip_keeps_pixel_centers_inside_the_rectangle(tmp_path: Path):
    path = tmp_path / "a.nc4"
    write_imerg_like(path, ["2020-01-01"], LATS, LONS)
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["Grid/precipitation"], ["a.nc4"])
    dec = netcdf.decode([path], raw, "")

    clipped = canonical.clip_bbox(dec.data, common_pb2.BBox(west=20.0, south=10.1, east=20.1, north=10.3))
    assert list(clipped["lat"].values) == [10.25, 10.15]
    assert list(clipped["lon"].values) == [20.05]

    # A rectangle smaller than a pixel keeps the pixel it touches.
    tiny = canonical.clip_bbox(dec.data, common_pb2.BBox(west=20.11, south=10.11, east=20.12, north=10.12))
    assert tiny.sizes["lat"] == 3 and tiny.sizes["lon"] == 2
