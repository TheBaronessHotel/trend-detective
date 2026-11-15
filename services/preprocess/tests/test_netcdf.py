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


def test_clip_keeps_centers_on_every_edge_despite_rounding():
    # MERRA-2's grid as Harmony returns it for 0 to 20 east and 42 to 55
    # north: 0 east comes as -5.9e-13, and centers sit on every edge.
    lons = np.array([-5.920304394294029e-13, 0.625, 10.0, 19.375, 20.000000000001])
    lats = np.array([55.0, 54.5, 42.5, 42.0])
    ds = xr.Dataset({"v": (("lat", "lon"), np.ones((len(lats), len(lons))))}, coords={"lat": lats, "lon": lons})

    clipped = canonical.clip_bbox(ds, common_pb2.BBox(west=0.0, south=42.0, east=20.0, north=55.0))
    assert clipped.sizes["lon"] == 5
    assert clipped.sizes["lat"] == 4

    # Centers more than the tolerance outside still go.
    inside = canonical.clip_bbox(ds, common_pb2.BBox(west=0.001, south=42.001, east=19.999, north=54.999))
    assert list(inside["lon"].values) == [0.625, 10.0, 19.375]
    assert list(inside["lat"].values) == [54.5, 42.5]


def test_many_threads_can_decode_at_once(tmp_path: Path):
    # The worker decodes two tasks' files in parallel threads; unguarded,
    # the netCDF C library crashed the process with a segfault.
    from concurrent.futures import ThreadPoolExecutor

    paths = []
    for i in range(8):
        path = tmp_path / f"{i}.nc4"
        write_imerg_like(path, ["2020-01-01", "2020-02-01"], LATS, LONS)
        paths.append(path)
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["Grid/precipitation"], [p.name for p in paths])

    def decode_one(path: Path) -> int:
        return netcdf.decode([path], raw, "").data.sizes["time"]

    with ThreadPoolExecutor(max_workers=8) as pool:
        sizes = list(pool.map(decode_one, paths * 4))
    assert sizes == [2] * 32


# Data on pressure levels, and axes found by name (PRE-6 review fixes).


def _jan_2020(raw: ingest_pb2.RawDataset) -> ingest_pb2.RawDataset:
    raw.files[0].coverage.CopyFrom(
        common_pb2.DateRange(start=common_pb2.Date(year=2020, month=1, day=1), end=common_pb2.Date(year=2020, month=1, day=31))
    )
    return raw


def test_a_single_pressure_level_is_a_plain_map(tmp_path: Path):
    times = pd.to_datetime(["2020-01-01"]).values
    lev = xr.DataArray([500.0], dims="lev", attrs={"units": "hPa"})
    da = xr.DataArray(
        np.ones((1, 1, 2, 2)), dims=("time", "lev", "lat", "lon"),
        coords={"time": times, "lev": lev, "lat": [1.0, 0.0], "lon": [0.0, 1.0]}, name="T",
    )
    path = tmp_path / "lev1.nc"
    da.to_dataset().to_netcdf(path)

    dec = netcdf.decode([path], raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["T"], ["lev1.nc"]), "K")

    assert dec.values.dims == ("time", "lat", "lon")


def test_an_axis_without_coordinate_values_is_refused(tmp_path: Path):
    import netCDF4 as nc

    path = tmp_path / "nocoords.nc"
    with nc.Dataset(path, "w") as f:
        f.createDimension("YDim", 3)
        f.createDimension("XDim", 2)
        f.createGroup("g").createVariable("V", "f4", ("YDim", "XDim"))[:] = np.arange(6).reshape(3, 2)
    raw = _jan_2020(raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["g/V"], ["nocoords.nc"]))

    with pytest.raises(errors.TaskFailure) as info:
        netcdf.decode([path], raw, "")
    assert info.value.code == errors.task_pb2.ERROR_CODE_UPSTREAM
    assert "no coordinate values" in info.value.detail


def _profile_file(path: Path, ps_value: float, ps_units: str | None) -> None:
    times = pd.to_datetime(["2020-01-01"]).values
    lev = xr.DataArray([1000.0, 500.0, 100.0], dims="lev", attrs={"units": "hPa"})
    vmr = xr.DataArray(
        np.array([1.0, 2.0, 3.0])[None, :, None, None] * np.ones((1, 3, 1, 1)), dims=("time", "lev", "lat", "lon"),
        coords={"time": times, "lev": lev, "lat": [0.0], "lon": [0.0]}, name="VMR",
    )
    ps = xr.DataArray(
        np.full((1, 1, 1), ps_value), dims=("time", "lat", "lon"),
        coords={"time": times, "lat": [0.0], "lon": [0.0]}, name="PS",
        attrs={"units": ps_units} if ps_units else {},
    )
    xr.Dataset({"VMR": vmr, "PS": ps}).to_netcdf(path)


def _column(path: Path):
    from google.protobuf import json_format

    from preprocess import filters
    from trenddetective.preprocess.v1 import preprocess_pb2

    dec = netcdf.decode([path], raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["VMR", "PS"], [path.name]), "")
    chain = json_format.ParseDict({"ops": [{"column_average": {"surface_pressure": "PS"}}]}, preprocess_pb2.FilterChain())
    filters.apply(dec, chain)
    return dec.values.values[0, 0, 0]


def test_surface_pressure_in_pa_is_converted(tmp_path: Path):
    path = tmp_path / "pa.nc"
    _profile_file(path, 60000.0, "Pa")
    # 600 hPa: the 1000 hPa layer is under the ground, weights 0, 300, 300.
    assert _column(path) == pytest.approx(2.5)


def test_surface_pressure_that_cant_be_hpa_is_refused(tmp_path: Path):
    path = tmp_path / "unlabelled.nc"
    _profile_file(path, 60000.0, None)
    with pytest.raises(errors.TaskFailure) as info:
        _column(path)
    assert "looks like Pa" in info.value.detail


def _write_nobm_like(path: Path, months_since: list[float], units: str = "months since 1998-01-01 00:00:00") -> None:
    """A file shaped like a HOSS subset of NOBM monthly chlorophyll: `tot` at
    the root, (time, lat, lon), and a time axis in months since 1998."""
    lats = np.array([17.5, 16.83, 16.17])
    lons = np.array([86.25, 87.5])
    data = np.arange(len(months_since) * 6, dtype="float32").reshape(len(months_since), 3, 2) / 10 + 0.1
    ds = xr.Dataset(
        {"tot": (("time", "lat", "lon"), data, {"units": "mg/m^3"})},
        coords={
            "time": ("time", np.array(months_since, dtype="float64"), {"units": units, "standard_name": "time"}),
            "lat": ("lat", lats, {"units": "degrees_north"}),
            "lon": ("lon", lons, {"units": "degrees_east"}),
        },
    )
    ds.to_netcdf(path, engine="netcdf4")


def test_months_since_a_date_become_months(tmp_path: Path):
    # NOBM counts months since January 1998, which xarray won't decode:
    # 180 is January 2013 and 181.5, a mid-month stamp, February.
    path = tmp_path / "nobm.nc4"
    _write_nobm_like(path, [180.0, 181.5])
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["tot"], ["nobm.nc4"])
    dec = netcdf.decode([path], raw, "mg/m³")
    assert [str(t)[:10] for t in dec.data["time"].values] == ["2013-01-01", "2013-02-01"]
    assert dec.step == common_pb2.TIME_STEP_MONTHLY
    assert dec.values.dims == ("time", "lat", "lon")


def test_years_since_a_date_become_years(tmp_path: Path):
    path = tmp_path / "yearly.nc4"
    _write_nobm_like(path, [0.0, 1.0], units="years since 2010-07-01")
    raw = raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["tot"], ["yearly.nc4"])
    dec = netcdf.decode([path], raw, "mg/m³")
    assert [str(t)[:10] for t in dec.data["time"].values] == ["2010-07-01", "2011-07-01"]
