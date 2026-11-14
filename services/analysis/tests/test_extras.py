from __future__ import annotations

import math

import numpy as np
import pytest
from google.protobuf import json_format

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.gateway.v1 import task_pb2

from conftest import ar1_noise, delhi, make_series, seasonal_series
from analysis import extras, regression, suite
from analysis.errors import TaskFailure


def plan(*extras_specs: str, correct: bool = True, regression_test: bool = True) -> analysis_pb2.TestPlan:
    """A plan with a regression and the named extras, at their defaults."""
    p = analysis_pb2.TestPlan()
    if regression_test:
        p.series.add().regression.correct_autocorrelation = correct
    for name in extras_specs:
        getattr(p.series.add(), name).SetInParent()
    return p


def shuffle_plan(permutations: int = 0, block_length: int = 0) -> analysis_pb2.TestPlan:
    p = plan("shuffle")
    p.series[1].shuffle.permutations = permutations
    p.series[1].shuffle.block_length = block_length
    return p


def triangle_plan(min_years: int = 0) -> analysis_pb2.TestPlan:
    p = plan("trend_triangle")
    p.series[1].trend_triangle.min_years = min_years
    return p


def matrix(triangle: results_pb2.TrendTriangleResult, values) -> np.ndarray:
    return np.array(values, dtype="float64").reshape(len(triangle.start_years), len(triangle.end_years))


def failure_code(series, test_plan) -> int:
    with pytest.raises(TaskFailure) as info:
        suite.run(series, test_plan)
    return info.value.code


# The shuffle test.


def test_shuffle_observed_slope_is_the_regression_slope():
    out = suite.run(delhi("T2M"), shuffle_plan())
    sh = out.shuffle
    assert sh.permutations == 1000 and sh.block_length == 12
    assert len(sh.shuffled_slopes_per_decade) == 1000
    assert sh.observed_slope_per_decade == pytest.approx(out.regression.slope_per_decade, rel=1e-9)
    steeper = sum(abs(s) >= abs(sh.observed_slope_per_decade) * (1 - 1e-9) for s in sh.shuffled_slopes_per_decade)
    assert sh.p_value == pytest.approx((steeper + 1) / 1001)


def test_shuffle_is_repeatable():
    first = suite.run(delhi("T2M"), shuffle_plan(permutations=300)).shuffle
    again = suite.run(delhi("T2M"), shuffle_plan(permutations=300)).shuffle
    assert list(first.shuffled_slopes_per_decade) == list(again.shuffled_slopes_per_decade)


def test_blocks_move_whole_and_the_tail_stays():
    rows = extras.block_orders(64, 12, 200, np.random.default_rng(1))
    assert rows.shape == (200, 64)
    for row in rows:
        assert sorted(row) == list(range(64))
        assert list(row[60:]) == [60, 61, 62, 63]
        for block in row[:60].reshape(5, 12):
            assert block[0] % 12 == 0 and list(block) == list(range(block[0], block[0] + 12))
    # 5 blocks have 120 orders, and 200 draws see many of them.
    assert len({tuple(row[::12]) for row in rows}) > 50


def test_shuffling_whole_years_keeps_the_seasons():
    # Every year is the same seasonal cycle, so moving whole years changes
    # nothing, not even with the raw values.
    values = 5 + np.tile(np.sin(2 * np.pi * np.arange(12) / 12), 10)
    p = shuffle_plan()
    p.field = analysis_pb2.SERIES_FIELD_VALUES
    sh = suite.run(make_series(values), p).shuffle
    assert np.allclose(sh.shuffled_slopes_per_decade, sh.observed_slope_per_decade, rtol=0, atol=1e-12)
    assert sh.p_value == 1.0


def test_a_strong_trend_beats_every_shuffle():
    values = seasonal_series(20, per_decade=2.0, sd=0.3, phi=0.3, seed=2)
    assert suite.run(make_series(values), shuffle_plan()).shuffle.p_value == pytest.approx(1 / 1001)


def test_noise_is_significant_about_one_time_in_twenty():
    # Trendless noise with autocorrelation. Whole-year blocks keep the
    # autocorrelation, so the test should cry wolf about 5% of the time,
    # where a plain t-test would much more often.
    hits = 0
    for seed in range(100):
        values = 10 + ar1_noise(240, 0.5, 1.0, seed=seed)
        hits += suite.run(make_series(values), shuffle_plan(permutations=200)).shuffle.p_value < 0.05
    assert 1 <= hits <= 12


def test_a_short_record_skips_the_shuffle():
    out = suite.run(make_series(seasonal_series(4, seed=3)), shuffle_plan())
    assert not out.HasField("shuffle")
    assert "The record is too short for the shuffle test, which needs at least 60 months." in out.warnings


@pytest.mark.parametrize("permutations, block_length", [(20_000, 12), (-5, 12), (100, -1)])
def test_shuffle_settings_are_checked(permutations: int, block_length: int):
    code = failure_code(make_series(seasonal_series(10, seed=1)), shuffle_plan(permutations, block_length))
    assert code == task_pb2.ERROR_CODE_INVALID_REQUEST


# The trend triangle.


def test_triangle_corner_is_the_whole_record():
    out = suite.run(delhi("T2M"), triangle_plan())
    tri = out.trend_triangle
    assert tri.min_years == 10
    assert list(tri.start_years) == list(range(1981, 2016))
    assert list(tri.end_years) == list(range(1990, 2025))
    slopes, p = matrix(tri, tri.slopes_per_decade), matrix(tri, tri.p_values)
    assert slopes[0, -1] == pytest.approx(out.regression.slope_per_decade)
    assert p[0, -1] == pytest.approx(out.regression.p_value)
    for i, start in enumerate(tri.start_years):
        for j, end in enumerate(tri.end_years):
            assert np.isfinite(slopes[i, j]) == (end - start + 1 >= 10)


def test_triangle_shows_a_peak():
    # Up for ten years, then down for ten, like NO2 over eastern China.
    k = np.arange(240)
    values = np.where(k < 120, k / 12, 20 - k / 12) + np.random.default_rng(4).normal(0, 0.3, k.size)
    tri = suite.run(make_series(values, start=(2005, 1)), triangle_plan(min_years=5)).trend_triangle
    slopes = matrix(tri, tri.slopes_per_decade)
    row = {year: i for i, year in enumerate(tri.start_years)}
    col = {year: j for j, year in enumerate(tri.end_years)}
    assert slopes[row[2005], col[2014]] == pytest.approx(10, abs=1)
    assert slopes[row[2015], col[2024]] == pytest.approx(-10, abs=1)
    assert abs(slopes[row[2005], col[2024]]) < 1


def test_without_a_regression_the_extras_use_the_correction():
    corrected = suite.run(delhi("T2M"), triangle_plan())
    alone = suite.run(delhi("T2M"), plan("trend_triangle", regression_test=False))
    assert not alone.HasField("regression")
    assert list(alone.trend_triangle.p_values) == pytest.approx(list(corrected.trend_triangle.p_values), nan_ok=True)


def test_a_short_record_skips_the_triangle():
    out = suite.run(make_series(seasonal_series(6, seed=5)), triangle_plan(min_years=10))
    assert not out.HasField("trend_triangle")
    assert "The record is shorter than the 10 years the trend triangle's shortest window needs." in out.warnings


def test_triangle_min_years_is_checked():
    code = failure_code(make_series(seasonal_series(10, seed=5)), triangle_plan(min_years=1))
    assert code == task_pb2.ERROR_CODE_INVALID_REQUEST


# A trend for each calendar month.


def test_monthly_trends_find_the_month_that_changes():
    years = 30
    values = np.random.default_rng(6).normal(0, 0.5, years * 12)
    values[6::12] += np.arange(years) * 0.1  # July only, 1 per decade
    out = suite.run(make_series(values), plan("monthly_trends"))
    trends = {t.month: t for t in out.monthly_trends}
    assert list(trends) == list(range(1, 13))
    assert trends[7].slope_per_decade == pytest.approx(1.0, abs=0.25)
    assert trends[7].significant
    assert [m for m, t in trends.items() if t.significant] == [7]
    assert suite.MONTHS_BY_CHANCE in out.warnings


def test_many_significant_months_get_no_caveat():
    out = suite.run(make_series(seasonal_series(30, per_decade=2.0, sd=0.3, seed=8)), plan("monthly_trends"))
    assert sum(t.significant for t in out.monthly_trends) >= 3
    assert suite.MONTHS_BY_CHANCE not in out.warnings


def test_months_seen_in_few_years_have_no_trend():
    out = suite.run(make_series(seasonal_series(4, seed=7)), plan("monthly_trends"))
    assert len(out.monthly_trends) == 12
    assert all(math.isnan(t.slope_per_decade) and not t.significant for t in out.monthly_trends)


# Years to significance.


def fit_with(slope: float, sd: float, phi: float) -> regression.LinearTrend:
    return regression.LinearTrend(
        n=120,
        n_effective=120,
        slope=slope,
        slope_stderr=0.01,
        intercept=0,
        intercept_stderr=0.01,
        residual_sd=sd,
        lag1_autocorrelation=phi,
        r_squared=0,
        p_value=0.5,
        ci_low=0,
        ci_high=0,
    )


@pytest.mark.parametrize("phi", [0.0, 0.5, -0.3])
def test_weatherhead_formula(phi: float):
    got = extras.years_to_significance(fit_with(0.1, 1.0, phi), years_so_far=20)
    assert got.years_needed == pytest.approx((3.3 * 1.0 / 0.1 * math.sqrt((1 + phi) / (1 - phi))) ** (2 / 3))
    assert (got.years_so_far, got.noise_sd, got.trend_per_year, got.phi) == (20, 1.0, 0.1, phi)


def test_no_trend_is_never_detected():
    assert extras.years_to_significance(fit_with(0.0, 1.0, 0.2), years_so_far=10).years_needed == math.inf
    # A flat series gets there through the whole suite, and protojson keeps it.
    out = suite.run(make_series(np.full(120, 3.0)), plan("years_to_significance"))
    back = json_format.Parse(json_format.MessageToJson(out), results_pb2.SeriesAnalysis())
    assert back.years_to_significance.years_needed == math.inf


def test_years_to_significance_follows_the_regression():
    out = suite.run(delhi("PRECTOTCORR"), plan("years_to_significance"))
    y, reg = out.years_to_significance, out.regression
    assert y.years_so_far == 44
    assert y.trend_per_year == pytest.approx(reg.slope_per_decade / 10)
    assert y.noise_sd == pytest.approx(reg.model.residual_sd)
    assert y.phi == pytest.approx(reg.model.lag1_autocorrelation)
    # The demo's story: Delhi rain has no trend yet, and a slope this small
    # needs a far longer record than 44 years.
    assert not y.already_significant
    assert y.years_needed > y.years_so_far


def test_a_strong_trend_is_detectable_already():
    out = suite.run(make_series(seasonal_series(20, per_decade=2.0, sd=0.3, seed=9)), plan("years_to_significance"))
    y = out.years_to_significance
    assert y.already_significant
    assert y.years_needed < y.years_so_far
