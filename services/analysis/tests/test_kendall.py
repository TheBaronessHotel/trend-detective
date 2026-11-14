"""Our Mann-Kendall code against the references: pymannkendall for the tests
and scipy.stats.theilslopes for Sen's slope and its interval."""

from __future__ import annotations

import numpy as np
import pymannkendall as pmk
import pytest
from scipy import stats

from conftest import ar1_noise, delhi, seasonal_series
from analysis import kendall
from analysis.series import own_anomalies


def years(n: int) -> np.ndarray:
    return np.arange(n) / 12.0


def calendar(n: int, first_month: int = 0) -> np.ndarray:
    return (first_month + np.arange(n)) % 12


def delhi_anomalies() -> np.ndarray:
    values = np.asarray(delhi("T2M").values)
    return own_anomalies(values, calendar(values.size))


def assert_same_test(got: kendall.KendallTest, want, slope_scale: float) -> None:
    assert got.s == want.s
    assert got.var_s == pytest.approx(want.var_s, rel=1e-9)
    assert got.z == pytest.approx(want.z, rel=1e-9)
    assert got.p_value == pytest.approx(want.p, rel=1e-6, abs=1e-12)
    assert got.tau == pytest.approx(want.Tau, rel=1e-9)
    # pymannkendall's slope is per step of its input; ours is per year.
    assert got.slope == pytest.approx(want.slope * slope_scale, rel=1e-9)


@pytest.mark.parametrize("seed", [1, 2, 3])
def test_original_matches_pymannkendall(seed: int):
    n = 240
    rng = np.random.default_rng(seed)
    # Rounding to one decimal makes plenty of ties.
    y = np.round(0.002 * np.arange(n) + rng.normal(0, 1, n), 1)
    got = kendall.mann_kendall(y, years(n), calendar(n), variant=kendall.ORIGINAL, alpha=0.05)
    assert_same_test(got, pmk.original_test(y, alpha=0.05), slope_scale=12)


@pytest.mark.parametrize("phi", [0.0, 0.5, 0.8])
def test_hamed_rao_matches_pymannkendall(phi: float):
    n = 300
    y = 0.003 * np.arange(n) + ar1_noise(n, phi, 1.0, seed=5)
    got = kendall.mann_kendall(y, years(n), calendar(n), variant=kendall.HAMED_RAO, alpha=0.05)
    assert_same_test(got, pmk.hamed_rao_modification_test(y, alpha=0.05), slope_scale=12)


def test_hamed_rao_matches_pymannkendall_on_real_data():
    y = delhi_anomalies()
    assert np.isfinite(y).all()
    got = kendall.mann_kendall(y, years(y.size), calendar(y.size), variant=kendall.HAMED_RAO, alpha=0.05)
    assert_same_test(got, pmk.hamed_rao_modification_test(y, alpha=0.05), slope_scale=12)


@pytest.mark.parametrize("first_month", [0, 3])
def test_seasonal_matches_pymannkendall_with_gaps(first_month: int):
    y = seasonal_series(20, per_decade=0.5, seed=4)
    y[[5, 17, 100, 101, 102, 230]] = np.nan
    n = y.size
    got = kendall.mann_kendall(y, years(n), calendar(n, first_month), variant=kendall.SEASONAL, alpha=0.05)
    # pymannkendall groups by position modulo 12, which is the same grouping
    # whatever month the series starts in. Its seasonal slope is per cycle,
    # which is per year here.
    assert_same_test(got, pmk.seasonal_test(y, period=12, alpha=0.05), slope_scale=1)
    assert got.n == n - 6


def test_sen_interval_matches_theilslopes_with_gaps():
    n = 180
    t = years(n)
    y = 0.05 * t + np.random.default_rng(8).normal(0, 1, n)
    y[[10, 11, 70]] = np.nan
    got = kendall.mann_kendall(y, t, calendar(n), variant=kendall.ORIGINAL, alpha=0.05)
    ok = np.isfinite(y)
    want = stats.theilslopes(y[ok], t[ok], alpha=0.95)
    assert got.slope == pytest.approx(want.slope, rel=1e-12)
    assert got.slope_ci_low == pytest.approx(want.low_slope, rel=1e-12)
    assert got.slope_ci_high == pytest.approx(want.high_slope, rel=1e-12)


def test_hamed_rao_widens_the_test_for_autocorrelated_data():
    n = 300
    y = 0.003 * np.arange(n) + ar1_noise(n, 0.8, 1.0, seed=9)
    plain = kendall.mann_kendall(y, years(n), calendar(n), variant=kendall.ORIGINAL, alpha=0.05)
    hr = kendall.mann_kendall(y, years(n), calendar(n), variant=kendall.HAMED_RAO, alpha=0.05)
    assert hr.slope == plain.slope
    assert hr.var_s > plain.var_s
    assert hr.p_value > plain.p_value
    assert hr.slope_ci_high - hr.slope_ci_low > plain.slope_ci_high - plain.slope_ci_low


def test_sen_slope_uses_real_times_across_a_gap():
    # A straight line with a year missing: squeezing the gap out would make
    # the line look steeper.
    t = years(60)
    y = 1.5 * t
    y[24:36] = np.nan
    got = kendall.mann_kendall(y, t, calendar(60), variant=kendall.ORIGINAL, alpha=0.05)
    assert got.slope == pytest.approx(1.5)


def test_no_change_gives_zero():
    got = kendall.mann_kendall(np.full(48, 2.0), years(48), calendar(48), variant=kendall.HAMED_RAO, alpha=0.05)
    assert got.s == 0 and got.z == 0 and got.p_value == 1.0 and got.slope == 0


def test_seasonal_needs_a_month_seen_twice():
    y = np.full(24, np.nan)
    y[:12] = np.arange(12)
    with pytest.raises(ValueError, match="two years"):
        kendall.mann_kendall(y, years(24), calendar(24), variant=kendall.SEASONAL, alpha=0.05)
