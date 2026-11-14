from __future__ import annotations

import numpy as np
import pandas as pd
import pytest
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from preprocess import canonical, errors, filters, output
from preprocess.canonical import Decoded

MONTHLY = common_pb2.TIME_STEP_MONTHLY
DAILY = common_pb2.TIME_STEP_DAILY


def point(values, start="2000-01-01", freq="MS", step=MONTHLY, units="°C") -> Decoded:
    times = pd.date_range(start, periods=len(values), freq=freq).values
    da = xr.DataArray(np.asarray(values, dtype="float64"), dims=("time",), coords={"time": times})
    return Decoded(data=canonical.make_dataset(da), units=units, step=step)


def chain(*ops: dict) -> preprocess_pb2.FilterChain:
    c = preprocess_pb2.FilterChain()
    for op in ops:
        (name, fields), = op.items()
        getattr(c.ops.add(), name).CopyFrom(type(getattr(preprocess_pb2.FilterOp(), name))(**fields))
    return c


def seasonal(years: int, trend_per_year: float = 0.0, amplitude: float = 10.0, base: float = 20.0) -> np.ndarray:
    m = np.arange(years * 12)
    return base + amplitude * np.sin(2 * np.pi * m / 12) + trend_per_year * m / 12


# Pixel steps


def test_qc_range_drops_impossible_values_and_keeps_old_reasons():
    dec = point([1.0, -5.0, np.nan, 200.0])
    filters.apply(dec, chain({"qc_range": {"min": 0, "max": 100}}))
    assert np.isnan(dec.values.values[[1, 2, 3]]).all()
    assert list(dec.reasons.values[1:]) == [
        data_pb2.MISSING_REASON_QC_FAILED,
        data_pb2.MISSING_REASON_FILL_VALUE,
        data_pb2.MISSING_REASON_QC_FAILED,
    ]


def test_linear_converts_and_renames_units():
    dec = point([273.15, 283.15], units="K")
    filters.apply(dec, chain({"linear": {"scale": 1, "offset": -273.15, "units": "°C"}}))
    assert dec.values.values == pytest.approx([0.0, 10.0])
    assert dec.units == "°C"


def test_linear_without_a_scale_keeps_values():
    dec = point([1.0, 2.0])
    filters.apply(dec, chain({"linear": {"offset": 1}}))
    assert list(dec.values.values) == [2.0, 3.0]


def test_log10_marks_non_positive_values():
    dec = point([100.0, 0.0, -1.0], units="mg/m³")
    filters.apply(dec, chain({"log10": {}}))
    assert dec.values.values[0] == 2.0
    assert np.isnan(dec.values.values[1:]).all()
    assert (dec.reasons.values[1:] == data_pb2.MISSING_REASON_NOT_POSITIVE).all()
    assert dec.units == "log₁₀(mg/m³)"


def test_monthly_mean_needs_enough_valid_days():
    days = pd.date_range("2020-01-01", "2020-02-29", freq="D")
    v = np.ones(len(days))
    v[25:31] = np.nan  # January: 25 valid days
    v[31 + 5 :] = np.nan  # February: 5 valid days
    dec = point(v, start="2020-01-01", freq="D", step=DAILY)

    filters.apply(dec, chain({"monthly_mean": {"min_valid_days": 20}}))

    assert dec.step == MONTHLY
    assert [str(t)[:10] for t in dec.data["time"].values] == ["2020-01-01", "2020-02-01"]
    assert dec.values.values[0] == 1.0
    assert np.isnan(dec.values.values[1])
    assert dec.reasons.values[1] == data_pb2.MISSING_REASON_TOO_FEW_DAYS


def test_monthly_mean_on_monthly_data_does_nothing():
    dec = point([1.0, 2.0])
    filters.apply(dec, chain({"monthly_mean": {"min_valid_days": 20}}))
    assert list(dec.values.values) == [1.0, 2.0]
    assert any("already monthly" in n for n in dec.notes)


def test_daily_data_gets_a_monthly_mean_before_log10():
    days = pd.date_range("2020-01-01", "2020-03-31", freq="D")
    dec = point(np.full(len(days), 10.0), start="2020-01-01", freq="D", step=DAILY, units="mg/m³")
    filters.apply(dec, chain({"qc_range": {"min": 0, "max": 100}}, {"log10": {}}))
    assert dec.applied == ["qc_range", "monthly_mean", "log10"]
    assert dec.step == MONTHLY
    assert list(dec.values.values) == [1.0, 1.0, 1.0]


def test_missing_months_are_filled_in_on_the_time_axis():
    times = pd.to_datetime(["2020-01-01", "2020-02-01", "2020-04-01"]).values
    da = xr.DataArray([1.0, 2.0, 4.0], dims=("time",), coords={"time": times})
    dec = Decoded(data=canonical.make_dataset(da), units="", step=MONTHLY)
    filters.apply(dec, preprocess_pb2.FilterChain())
    assert len(dec.data["time"]) == 4
    assert np.isnan(dec.values.values[2])
    assert dec.reasons.values[2] == data_pb2.MISSING_REASON_UNSPECIFIED


def test_coarsen_averages_blocks():
    times = pd.to_datetime(["2020-01-01"]).values
    da = xr.DataArray(
        np.arange(16.0).reshape(1, 4, 4), dims=("time", "lat", "lon"),
        coords={"time": times, "lat": [3.5, 2.5, 1.5, 0.5], "lon": [0.5, 1.5, 2.5, 3.5]},
    )
    dec = Decoded(data=canonical.make_dataset(da), units="", step=MONTHLY)
    filters.apply(dec, chain({"coarsen": {"factor": 2}}))
    assert dec.values.shape == (1, 2, 2)
    assert dec.values.values[0, 0, 0] == pytest.approx((0 + 1 + 4 + 5) / 4)


# Chain checks


@pytest.mark.parametrize(
    "ops",
    [
        ({"anomaly": {}}, {"log10": {}}),
        ({"anomaly": {}}, {"anomaly": {}}),
        ({"monthly_mean": {}}, {"monthly_mean": {}}),
    ],
)
def test_chains_that_cant_run_are_refused(ops):
    with pytest.raises(errors.TaskFailure) as info:
        filters.apply(point(seasonal(3)), chain(*ops))
    assert info.value.code == errors.task_pb2.ERROR_CODE_INVALID_REQUEST


def test_an_empty_step_is_refused():
    c = preprocess_pb2.FilterChain()
    c.ops.add()
    with pytest.raises(errors.TaskFailure):
        filters.apply(point([1.0]), c)


def test_percent_anomaly_on_temperature_is_refused():
    with pytest.raises(errors.TaskFailure):
        filters.apply(point(seasonal(3), units="°C"), chain({"anomaly": {"kind": data_pb2.ANOMALY_KIND_PERCENT}}))


# Anomalies


def years_of(dec: Decoded) -> np.ndarray:
    seconds = dec.data["time"].values.astype("datetime64[s]").astype("int64")
    return seconds / (365.25 * 86400)


def joint_normals(v: np.ndarray, t: np.ndarray) -> tuple[np.ndarray, float]:
    """The normals and trend of a least-squares fit with one constant per
    calendar month and a straight line, the seasons taken at the mean time."""
    ok = ~np.isnan(v)
    months = np.arange(v.size) % 12
    design = np.column_stack([months == m for m in range(12)] + [t - t[ok].mean()]).astype(float)
    coef, *_ = np.linalg.lstsq(design[ok], v[ok], rcond=None)
    return coef[:12], coef[12]


def test_absolute_anomalies_are_the_values_less_the_seasons():
    dec = point(seasonal(10, trend_per_year=0.3) + np.random.default_rng(1).normal(0, 1, 120))
    v = dec.values.values.copy()
    filters.apply(dec, chain({"anomaly": {"kind": data_pb2.ANOMALY_KIND_ABSOLUTE}}))

    normals, trend = joint_normals(v, years_of(dec))
    assert trend == pytest.approx(0.3, abs=0.1)
    assert dec.anomalies.values == pytest.approx(v - normals[np.arange(120) % 12], abs=1e-9)
    assert dec.anomaly.baseline.start_year == 2000 and dec.anomaly.baseline.end_year == 2009
    # The values themselves are kept.
    assert dec.values.values[0] == pytest.approx(seasonal(10, 0.3)[0], abs=5)


@pytest.mark.parametrize("method", [data_pb2.CLIMATOLOGY_METHOD_MONTHLY_MEANS, data_pb2.CLIMATOLOGY_METHOD_HARMONIC])
def test_anomalies_keep_the_whole_trend(method):
    # A strong, smooth trend like CO2's over 7 years. Normals that took in the
    # trend's rise within each year would leave a staircase: a slope 2% low
    # and a sawtooth of about 1 ppm around it.
    dec = point(seasonal(7, trend_per_year=2.5, amplitude=3.0, base=400.0), units="ppm")
    filters.apply(dec, chain({"anomaly": {"method": method, "harmonics": 1}}))
    t = years_of(dec)
    line = 2.5 * (t - t.mean())
    assert np.abs(dec.anomalies.values - line).max() < 0.02
    assert np.polyfit(t, dec.anomalies.values, 1)[0] == pytest.approx(2.5, rel=0.002)


def test_baseline_limits_the_years_normals_come_from():
    v = np.concatenate([np.full(60, 10.0), np.full(60, 20.0)])
    dec = point(v)
    filters.apply(dec, chain({"anomaly": {"baseline": {"start_year": 2000, "end_year": 2004}}}))
    assert dec.anomalies.values[0] == 0.0
    assert dec.anomalies.values[-1] == 10.0
    assert (dec.anomaly.baseline.start_year, dec.anomaly.baseline.end_year) == (2000, 2004)


def test_a_month_without_enough_baseline_years_has_no_anomaly():
    v = seasonal(10)
    v[[0, 12, 24, 36, 48, 60]] = np.nan  # January missing in 6 of 10 years
    dec = point(v)
    filters.apply(dec, chain({"anomaly": {}}))
    jan = dec.anomalies.values[::12]
    assert np.isnan(jan).all()
    assert not np.isnan(dec.anomalies.values[1::12]).any()


def test_percent_anomaly_skips_near_zero_normals():
    v = np.tile([100.0, 100, 100, 100, 100, 0.1, 100, 100, 100, 100, 100, 100], 5)
    v = v * (1 + 0.1 * np.repeat(np.arange(5), 12))
    dec = point(v, units="mm/day")
    filters.apply(dec, chain({"anomaly": {"kind": data_pb2.ANOMALY_KIND_PERCENT}}))
    june = dec.anomalies.values[5::12]
    assert np.isnan(june).all()
    # The values grow 10% a year, so January's normal is its value at the
    # middle of the record, not its plain mean.
    january = joint_normals(v, years_of(dec))[0][0]
    assert dec.anomalies.values[0] == pytest.approx((100 - january) / january * 100)


def test_standardized_anomaly_skips_months_without_spread():
    v = seasonal(5) + np.tile(np.r_[0.0, np.ones(11)], 5) * np.repeat(np.arange(5.0), 12)
    dec = point(v)
    filters.apply(dec, chain({"anomaly": {"kind": data_pb2.ANOMALY_KIND_STANDARDIZED}}))
    assert np.isnan(dec.anomalies.values[::12]).all()  # January never changes
    assert not np.isnan(dec.anomalies.values[1::12]).any()


def test_harmonic_normals_follow_a_smooth_cycle():
    dec = point(seasonal(4))
    filters.apply(dec, chain({"anomaly": {"method": data_pb2.CLIMATOLOGY_METHOD_HARMONIC, "harmonics": 1}}))
    assert np.abs(dec.anomalies.values).max() < 1e-9


def test_grid_anomalies_are_per_pixel():
    times = pd.date_range("2000-01-01", periods=36, freq="MS").values
    base = seasonal(3)
    v = np.stack([base, base + 100], axis=1).reshape(36, 1, 2)
    da = xr.DataArray(v, dims=("time", "lat", "lon"), coords={"time": times, "lat": [0.5], "lon": [0.5, 1.5]})
    dec = Decoded(data=canonical.make_dataset(da), units="kg/m²", step=MONTHLY)
    filters.apply(dec, chain({"anomaly": {}}))
    assert np.abs(dec.anomalies.values).max() < 1e-9


# Series output


def test_series_carries_anomalies_normals_and_display_line():
    v = seasonal(5, trend_per_year=1.0)
    v[[13, 14, 30, 31, 32]] = np.nan
    dec = point(v)
    filters.apply(dec, chain({"anomaly": {}}, {"gap_fill_display": {"max_gap": 2}}))
    raw = ingest_pb2.RawDataset(variable_id="air-temperature-power", source=ingest_pb2.SOURCE_POWER)

    series = output.region_series(dec, raw, raw.variable_id, preprocess_pb2.Aggregation())

    assert len(series.anomalies) == len(series.display_values) == 60
    assert series.anomaly_kind == data_pb2.ANOMALY_KIND_ABSOLUTE
    assert [n.month for n in series.normals] == list(range(1, 13))
    assert all(n.usable for n in series.normals)
    assert (series.baseline.start_year, series.baseline.end_year) == (2000, 2004)
    # A 2-step gap is drawn, a 3-step gap isn't.
    assert not np.isnan(series.display_values[13]) and not np.isnan(series.display_values[14])
    assert np.isnan(series.display_values[31])
    assert np.isnan(series.values[13])
    # Every missing anomaly is a missing value or an unusable month.
    for i, a in enumerate(series.anomalies):
        if np.isnan(a):
            assert np.isnan(series.values[i])

    csv = output.series_csv(series).decode().splitlines()
    assert csv[0] == "time,value (°C),anomaly,coverage,missing_reason"


def test_gap_fill_leaves_the_ends_empty():
    out = output.gap_fill(np.array([np.nan, 1.0, np.nan, 3.0, np.nan]), 2)
    assert np.isnan(out[0]) and np.isnan(out[-1])
    assert out[2] == 2.0


def test_monthly_data_stamped_mid_month_keeps_its_values():
    times = pd.to_datetime(["2020-01-15", "2020-02-15", "2020-04-15"]).values
    da = xr.DataArray([1.0, 2.0, 4.0], dims=("time",), coords={"time": times})
    dec = Decoded(data=canonical.make_dataset(da), units="", step=MONTHLY)
    filters.apply(dec, preprocess_pb2.FilterChain())
    assert [str(t)[:10] for t in dec.data["time"].values] == ["2020-01-01", "2020-02-01", "2020-03-01", "2020-04-01"]
    assert dec.values.values[[0, 1, 3]].tolist() == [1.0, 2.0, 4.0]
    assert np.isnan(dec.values.values[2])


@pytest.mark.parametrize("qc", [{"min": 0}, {"min": 10, "max": 5}, {}])
def test_qc_range_without_a_usable_max_is_refused(qc):
    with pytest.raises(errors.TaskFailure) as info:
        filters.apply(point([5.0, 10.0]), chain({"qc_range": qc}))
    assert "qc_range" in info.value.detail
