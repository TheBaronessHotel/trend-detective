"""column_average and data on pressure levels.

The expected numbers are worked by hand. For levels 1000, 500 and 100 hPa the
layers are 1000: from the surface to 750, 500: 750 to 300, 100: 300 to 0.
"""

from __future__ import annotations

import numpy as np
import pandas as pd
import pytest
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from preprocess import canonical, errors, filters
from preprocess.canonical import Decoded
from preprocess.filters import column

PS = "ascending/Data_Fields/SurfPres_Forecast_A"


def profile(vmr, surface_pressure, levels=(1000.0, 500.0, 100.0)) -> Decoded:
    """One month on a 1 × len(surface_pressure) grid. vmr is (pixel, level)."""
    vmr = np.asarray(vmr, dtype="float64")
    ps = np.asarray(surface_pressure, dtype="float64")
    times = pd.to_datetime(["2020-01-01"]).values
    lons = np.arange(len(ps), dtype="float64")
    v = xr.DataArray(
        vmr.T[None, :, None, :],
        dims=("time", "level", "lat", "lon"),
        coords={"time": times, "level": list(levels), "lat": [0.0], "lon": lons},
    )
    ds = canonical.make_dataset(v)
    ds[canonical.helper_name(PS)] = xr.DataArray(
        ps[None, None, :], dims=("time", "lat", "lon"), coords={"time": times, "lat": [0.0], "lon": lons}
    )
    return Decoded(data=ds, units="mol/mol", step=common_pb2.TIME_STEP_MONTHLY)


def chain(**column_fields) -> preprocess_pb2.FilterChain:
    c = preprocess_pb2.FilterChain()
    c.ops.add().column_average.CopyFrom(preprocess_pb2.ColumnAverage(surface_pressure=PS, **column_fields))
    return c


def test_layer_bounds_in_any_order():
    bottom, top = column.layer_bounds(np.array([100.0, 1000.0, 500.0]))
    assert list(bottom) == [300.0, np.inf, 750.0]
    assert list(top) == [0.0, 750.0, 300.0]


def test_column_average_weights_levels_by_their_air():
    dec = profile([[1.0, 2.0, 3.0], [1.0, 2.0, 3.0]], [900.0, 600.0])
    filters.apply(dec, chain())
    v = dec.values.values[0, 0]
    # 900 hPa: weights 150, 450, 300. 600 hPa: the 1000 hPa layer is under
    # the ground, weights 0, 300, 300.
    assert v[0] == pytest.approx((150 * 1 + 450 * 2 + 300 * 3) / 900)
    assert v[1] == pytest.approx((300 * 2 + 300 * 3) / 600)
    assert dec.values.dims == ("time", "lat", "lon")
    assert list(dec.data.data_vars) == ["value", "reason"]  # the helper is gone


def test_a_constant_profile_averages_to_itself():
    dec = profile([[1.85e-6] * 3], [1013.0])
    filters.apply(dec, chain())
    assert dec.values.values[0, 0, 0] == pytest.approx(1.85e-6)


def test_levels_under_the_ground_dont_count_against_coverage():
    # Like AIRS: the 1000 hPa level is empty because the ground is at 600.
    dec = profile([[np.nan, 2.0, 3.0]], [600.0])
    filters.apply(dec, chain())
    assert dec.values.values[0, 0, 0] == pytest.approx(2.5)


def test_too_little_of_the_column_is_a_failed_quality_check():
    # The 500 hPa layer holds 450 of 900: half the column is missing.
    dec = profile([[1.0, np.nan, 3.0], [np.nan, np.nan, np.nan]], [900.0, 900.0])
    filters.apply(dec, chain())
    assert np.isnan(dec.values.values[0, 0]).all()
    assert dec.reasons.values[0, 0, 0] == data_pb2.MISSING_REASON_QC_FAILED
    assert dec.reasons.values[0, 0, 1] == data_pb2.MISSING_REASON_FILL_VALUE
    # With a lower bar the first pixel counts.
    dec = profile([[1.0, np.nan, 3.0]], [900.0])
    filters.apply(dec, chain(min_coverage=0.4))
    assert dec.values.values[0, 0, 0] == pytest.approx((150 * 1 + 300 * 3) / 450)


def test_missing_surface_pressure_makes_the_pixel_missing():
    dec = profile([[1.0, 2.0, 3.0]], [np.nan])
    filters.apply(dec, chain())
    assert np.isnan(dec.values.values[0, 0, 0])


def test_column_then_unit_conversion_then_anomaly():
    times = pd.date_range("2018-01-01", periods=36, freq="MS").values
    v = xr.DataArray(
        np.full((36, 3, 1, 1), 1.8e-6) + np.arange(36)[:, None, None, None] * 1e-9,
        dims=("time", "level", "lat", "lon"),
        coords={"time": times, "level": [1000.0, 500.0, 100.0], "lat": [0.0], "lon": [0.0]},
    )
    ds = canonical.make_dataset(v)
    ds[canonical.helper_name(PS)] = xr.DataArray(
        np.full((36, 1, 1), 1000.0), dims=("time", "lat", "lon"), coords={"time": times, "lat": [0.0], "lon": [0.0]}
    )
    dec = Decoded(data=ds, units="mol/mol", step=common_pb2.TIME_STEP_MONTHLY)
    c = chain()
    c.ops.add().linear.CopyFrom(preprocess_pb2.Linear(scale=1e9, units="ppb"))
    c.ops.add().anomaly.CopyFrom(preprocess_pb2.Anomaly())
    filters.apply(dec, c)
    assert dec.units == "ppb"
    assert dec.values.values[0, 0, 0] == pytest.approx(1800.0)
    assert dec.anomalies is not None


def test_data_left_on_levels_is_refused():
    dec = profile([[1.0, 2.0, 3.0]], [1000.0])
    with pytest.raises(errors.TaskFailure) as info:
        filters.apply(dec, preprocess_pb2.FilterChain())
    assert info.value.code == errors.task_pb2.ERROR_CODE_UNSUPPORTED
    assert "column_average" in info.value.detail


def test_column_average_needs_levels_and_its_helper():
    times = pd.to_datetime(["2020-01-01"]).values
    flat = Decoded(
        data=canonical.make_dataset(xr.DataArray([1.0], dims="time", coords={"time": times})),
        units="",
        step=common_pb2.TIME_STEP_MONTHLY,
    )
    with pytest.raises(errors.TaskFailure, match="."):
        filters.apply(flat, chain())

    dec = profile([[1.0, 2.0, 3.0]], [1000.0])
    c = preprocess_pb2.FilterChain()
    c.ops.add().column_average.CopyFrom(preprocess_pb2.ColumnAverage(surface_pressure="nope"))
    with pytest.raises(errors.TaskFailure) as info:
        filters.apply(dec, c)
    assert "nope" in info.value.detail


def test_column_average_twice_is_refused():
    c = chain()
    c.ops.add().column_average.CopyFrom(preprocess_pb2.ColumnAverage(surface_pressure=PS))
    with pytest.raises(errors.TaskFailure):
        filters.apply(profile([[1.0, 2.0, 3.0]], [1000.0]), c)


def test_monthly_mean_keeps_helpers():
    days = pd.date_range("2020-01-01", "2020-01-31", freq="D").values
    v = xr.DataArray(
        np.ones((31, 3, 1, 1)), dims=("time", "level", "lat", "lon"),
        coords={"time": days, "level": [1000.0, 500.0, 100.0], "lat": [0.0], "lon": [0.0]},
    )
    ds = canonical.make_dataset(v)
    ds[canonical.helper_name(PS)] = xr.DataArray(
        np.full((31, 1, 1), 950.0), dims=("time", "lat", "lon"), coords={"time": days, "lat": [0.0], "lon": [0.0]}
    )
    dec = Decoded(data=ds, units="", step=common_pb2.TIME_STEP_DAILY)
    filters.apply(dec, chain())
    assert dec.applied == ["monthly_mean", "column_average"]
    assert dec.values.values[0, 0, 0] == pytest.approx(1.0)
