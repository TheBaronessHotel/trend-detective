from __future__ import annotations

import math

import numpy as np
import pytest
from scipy import stats

from conftest import ar1_noise
from analysis.regression import fit, lag1_autocorrelation


def monthly_t(n: int) -> np.ndarray:
    t = np.arange(n) / 12.0
    return t - t.mean()


def test_without_correction_it_is_plain_least_squares():
    n = 240
    t = monthly_t(n)
    y = 0.03 * t + np.random.default_rng(3).normal(0, 0.5, n)

    got = fit(t, y, alpha=0.05, correct_autocorrelation=False)
    want = stats.linregress(t, y)

    assert got.n == n and got.n_effective == n
    assert got.slope == pytest.approx(want.slope, rel=1e-12)
    assert got.slope_stderr == pytest.approx(want.stderr, rel=1e-12)
    assert got.intercept == pytest.approx(want.intercept, rel=1e-12)
    assert got.intercept_stderr == pytest.approx(want.intercept_stderr, rel=1e-12)
    assert got.p_value == pytest.approx(want.pvalue, rel=1e-9)
    assert got.r_squared == pytest.approx(want.rvalue**2, rel=1e-12)
    half = stats.t.ppf(0.975, n - 2) * want.stderr
    assert (got.ci_low, got.ci_high) == pytest.approx((want.slope - half, want.slope + half), rel=1e-12)


def test_correction_follows_santer():
    n = 300
    t = monthly_t(n)
    y = 0.05 * t + ar1_noise(n, 0.8, 1.0, seed=7)

    plain = fit(t, y, alpha=0.05, correct_autocorrelation=False)
    got = fit(t, y, alpha=0.05, correct_autocorrelation=True)

    r1 = got.lag1_autocorrelation
    assert 0.6 < r1 < 0.95
    n_eff = n * (1 - r1) / (1 + r1)
    assert got.n_effective == pytest.approx(n_eff)
    assert got.slope == plain.slope
    assert got.slope_stderr == pytest.approx(plain.slope_stderr * math.sqrt((n - 2) / (n_eff - 2)))
    t_stat = got.slope / got.slope_stderr
    assert got.p_value == pytest.approx(2 * stats.t.sf(abs(t_stat), n_eff - 2))
    assert got.p_value > plain.p_value
    assert got.ci_high - got.ci_low > plain.ci_high - plain.ci_low


def test_negative_autocorrelation_isnt_corrected():
    n = 120
    t = monthly_t(n)
    y = np.where(np.arange(n) % 2 == 0, 1.0, -1.0) + 0.01 * t
    got = fit(t, y, alpha=0.05, correct_autocorrelation=True)
    assert got.lag1_autocorrelation < 0
    assert got.n_effective == n


def test_gaps_keep_the_real_times():
    n = 120
    t = monthly_t(n)
    y = 2.0 * t + 5.0
    y[[3, 4, 5, 50, 90]] = np.nan

    got = fit(t, y, alpha=0.05, correct_autocorrelation=True)

    assert got.n == n - 5
    assert got.slope == pytest.approx(2.0)
    assert got.intercept == pytest.approx(5.0)
    # Only rounding error is left in the residuals.
    assert got.p_value < 1e-9
    assert got.r_squared == pytest.approx(1.0)


def test_a_flat_series_has_no_trend():
    t = monthly_t(60)
    got = fit(t, np.full(60, 3.0), alpha=0.05, correct_autocorrelation=True)
    assert got.slope == 0.0
    assert got.p_value == 1.0
    assert got.r_squared == 0.0


def test_lag1_without_gaps_is_the_usual_estimate():
    x = ar1_noise(200, 0.6, 1.0, seed=11)
    d = x - x.mean()
    assert lag1_autocorrelation(x) == pytest.approx(float(d[:-1] @ d[1:] / (d @ d)))


def test_lag1_pairs_only_real_neighbors():
    # Real neighbors are always equal. Squeezing the gaps out would pair 1
    # with -1 half the time and give 1/8 instead.
    x = np.array([1, 1, np.nan, -1, -1, np.nan, 1, 1, np.nan, -1, -1], dtype="float64")
    # The usual biased estimate: 8 values, so (n - 1) / n for a perfect match.
    assert lag1_autocorrelation(x) == pytest.approx(7 / 8)
    assert lag1_autocorrelation(np.array([1.0, np.nan, 2.0, np.nan, 3.0])) == 0.0
