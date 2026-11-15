from __future__ import annotations

import math

import numpy as np
import pytest

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.gateway.v1 import task_pb2

from conftest import ar1_noise, delhi, make_series
from analysis import diagnostics, suite
from analysis.errors import TaskFailure
from analysis.series import month_timestamp

PASS, WARN = results_pb2.CHECK_STATUS_PASS, results_pb2.CHECK_STATUS_WARN


def plan(*names: str, correct: bool = True, mann_kendall: bool = False) -> analysis_pb2.TestPlan:
    """A plan with a regression, optionally Mann-Kendall, and the named
    diagnostics at their defaults."""
    p = analysis_pb2.TestPlan()
    p.series.add().regression.correct_autocorrelation = correct
    if mann_kendall:
        p.series.add().mann_kendall.SetInParent()
    for name in names:
        getattr(p.series.add(), name).SetInParent()
    return p


def month_of(year: int, month: int) -> int:
    return year * 12 + month - 1


# Pettitt.


def brute_force_u(x: np.ndarray) -> np.ndarray:
    n = x.size
    return np.array([sum(np.sign(x[i] - x[j]) for i in range(t) for j in range(t, n)) for t in range(1, n)])


@pytest.mark.parametrize("seed", [1, 2, 3])
def test_pettitt_matches_its_definition(seed: int):
    x = np.round(np.random.default_rng(seed).normal(0, 1, 40), 1)  # with ties
    u = brute_force_u(x)
    t = int(np.argmax(np.abs(u)))
    k = abs(u[t])
    got = diagnostics.pettitt(x, line_sse=math.inf)
    assert got.k == k
    assert got.index == t + 1
    assert got.p_value == pytest.approx(min(1, 2 * math.exp(-6 * k**2 / (40**3 + 40**2))))
    assert got.mean_before == pytest.approx(x[: t + 1].mean())
    assert got.mean_after == pytest.approx(x[t + 1 :].mean())


def test_pettitt_finds_a_step_and_says_so():
    values = np.random.default_rng(5).normal(0, 0.5, 120)
    values[60:] += 2.0
    out = suite.run(make_series(values, start=(2000, 1)), plan("pettitt"))
    p = out.pettitt
    assert p.significant
    assert p.change_time == month_timestamp(month_of(2005, 1))
    assert p.mean_after - p.mean_before == pytest.approx(2.0, abs=0.2)
    assert any(w.startswith("The values jump around January 2005") for w in out.warnings)


def test_a_steady_trend_is_not_a_step():
    values = np.arange(240) / 12 * 0.2 + np.random.default_rng(6).normal(0, 0.5, 240)
    out = suite.run(make_series(values), plan("pettitt"))
    # Pettitt finds a "change" in the middle of any steady trend, but the
    # straight line fits better, so there's no caveat.
    assert out.pettitt.significant
    assert not any(w.startswith("The values jump") for w in out.warnings)


def test_pettitt_points_at_a_real_month_across_gaps():
    values = np.random.default_rng(7).normal(0, 0.3, 120)
    values[60:] += 3.0
    values[58:62] = np.nan  # the first value after the step is slot 62
    out = suite.run(make_series(values, start=(2000, 1)), plan("pettitt"))
    assert out.pettitt.change_time == month_timestamp(month_of(2000, 1) + 62)


def test_pettitt_allows_for_autocorrelation():
    x = np.round(np.random.default_rng(4).normal(0, 1, 40), 1)
    plain = diagnostics.pettitt(x, line_sse=math.inf)
    corrected = diagnostics.pettitt(x, line_sse=math.inf, r1=0.5)
    # The same split, with the variance in the exponent (1 + 0.5) / (1 - 0.5) = 3 times as large.
    assert (corrected.k, corrected.index) == (plain.k, plain.index)
    assert corrected.p_value == pytest.approx(min(1, 2 * math.exp(-6 * plain.k**2 / ((40**3 + 40**2) * 3))))
    # As in the regression, only a positive r1 is allowed for.
    assert diagnostics.pettitt(x, line_sse=math.inf, r1=-0.3).p_value == plain.p_value


def test_pettitt_rarely_finds_a_step_in_autocorrelated_noise():
    # Trendless months that resemble their neighbors, like real anomalies.
    # Before the allowance for autocorrelation, 41 of these 100 came out with
    # a significant change and the caveat about a jump; with it, 2 do.
    flagged = jumps = 0
    for seed in range(100):
        out = suite.run(make_series(ar1_noise(240, 0.5, 1.0, seed)), plan("pettitt"))
        flagged += out.pettitt.significant
        jumps += any(w.startswith("The values jump") for w in out.warnings)
    assert flagged <= 10
    assert jumps <= 10


def test_the_allowance_follows_the_plans_correction():
    values = ar1_noise(240, 0.5, 1.0, 3)
    values[120:] += 0.5
    on = suite.run(make_series(values), plan("pettitt")).pettitt.p_value
    off = suite.run(make_series(values), plan("pettitt", correct=False)).pettitt.p_value
    assert off < on < 1


# Residual checks.


def test_white_noise_passes():
    values = np.random.default_rng(11).normal(0, 1, 240)
    rc = suite.run(make_series(values), plan("residual_checks")).residual_checks
    assert rc.autocorrelation == PASS and rc.normality == PASS
    assert rc.normality_p_value >= 0.05


def test_autocorrelation_warns_and_explains_itself_without_the_correction():
    values = ar1_noise(240, 0.7, 1.0, seed=12)
    corrected = suite.run(make_series(values), plan("residual_checks"))
    plain = suite.run(make_series(values), plan("residual_checks", correct=False))
    for out in (corrected, plain):
        assert out.residual_checks.autocorrelation == WARN
        assert out.residual_checks.lag1_autocorrelation == pytest.approx(out.regression.model.lag1_autocorrelation)
    # With the correction on, the regression already allows for it.
    assert suite.UNCORRECTED_AUTOCORRELATION not in corrected.warnings
    assert suite.UNCORRECTED_AUTOCORRELATION in plain.warnings


def test_heavy_tails_point_to_mann_kendall():
    values = np.random.default_rng(13).standard_t(2, 240)
    with_mk = suite.run(make_series(values), plan("residual_checks", mann_kendall=True))
    alone = suite.run(make_series(values), plan("residual_checks"))
    assert with_mk.residual_checks.normality == WARN
    assert suite.NOT_BELL_SHAPED_MK in with_mk.warnings
    assert suite.NOT_BELL_SHAPED in alone.warnings


# Outliers.


def test_robust_z_follows_iglewicz_and_hoaglin():
    x = np.array([1.0, 2.0, 3.0, 4.0, 100.0, np.nan])
    median, mad = 3.0, 1.0
    want = 0.6745 * (x[:5] - median) / mad
    assert diagnostics.robust_z(x)[:5] == pytest.approx(want, rel=1e-6)
    assert math.isnan(diagnostics.robust_z(x)[5])
    # Over half the values equal the median: the mean absolute deviation stands in.
    y = np.array([5.0, 5.0, 5.0, 5.0, 7.0])
    assert diagnostics.robust_z(y)[4] == pytest.approx(2.0 / (1.253314 * 0.4))
    assert list(diagnostics.robust_z(np.full(4, 2.0))) == [0, 0, 0, 0]


def test_a_spike_is_flagged():
    values = np.random.default_rng(14).normal(0, 1, 120)
    values[50] = 9.0
    o = suite.run(make_series(values, start=(2000, 1)), plan("outliers")).outliers
    assert o.robust_z_threshold == 3.5
    assert [x.time for x in o.outliers] == [month_timestamp(month_of(2000, 1) + 50)]
    assert o.outliers[0].robust_z > 3.5
    assert o.highest.time == o.outliers[0].time
    assert o.lowest.value == pytest.approx(np.min(suite.prepare(make_series(values), 0).tested))


def test_a_month_is_judged_against_the_same_month():
    rng = np.random.default_rng(15)
    values = rng.normal(0, 0.5, 240)
    july = np.arange(240) % 12 == 6
    values[july] = rng.normal(0, 5, july.sum())  # July scatters ten times as much
    values[6 + 12 * 10] = 6.0  # a July within July's usual range
    values[12 * 15] = 3.0  # a January far outside January's
    flagged = {x.time.ToDatetime().strftime("%Y-%m") for x in suite.run(make_series(values), plan("outliers")).outliers.outliers}
    assert "1996-01" in flagged
    assert "1991-07" not in flagged


def test_a_nearly_constant_month_isnt_full_of_outliers():
    # November is nearly always dry, so its own spread is tiny. A little rain
    # in one November is unusual for November but not worth flagging.
    rng = np.random.default_rng(16)
    values = rng.gamma(2.0, 1.5, 240)
    november = np.arange(240) % 12 == 10
    values[november] = 0.0
    values[10 + 12 * 7] = 0.8
    out = suite.run(make_series(values), plan("outliers"))
    assert all(x.time.ToDatetime().month != 11 for x in out.outliers.outliers)


def test_a_short_record_is_scored_against_itself():
    values = np.random.default_rng(17).normal(0, 1, 36)
    values[20] = 8.0
    z = diagnostics.seasonal_robust_z(values, np.arange(36) % 12)
    assert np.isfinite(z).all()
    assert np.argmax(np.abs(z)) == 20


@pytest.mark.parametrize("trend_survives", [True, False])
def test_outliers_that_decide_the_verdict_get_a_caveat(trend_survives: bool):
    rng = np.random.default_rng(18)
    values = rng.normal(0, 0.4, 120)
    # Three huge months, apart. Side by side they would make the residuals
    # autocorrelated, and the corrected regression would discount them.
    late = [-2, -9, -16]
    if trend_survives:
        # No trend, but the late months make one.
        values[late] += 8.0
        wanted = "The trend rests on 3 unusual months: without them, the trend isn't significant."
    else:
        # A real trend, hidden by the late months.
        values += np.arange(120) / 12 * 0.08
        values[late] -= 8.0
        wanted = "3 unusual months hide the trend: without them, the trend would be significant."
    out = suite.run(make_series(values), plan("outliers"))
    assert len(out.outliers.outliers) == 3
    assert wanted in out.warnings


def test_threshold_is_checked():
    p = plan("outliers")
    p.series[1].outliers.robust_z_threshold = -1
    with pytest.raises(TaskFailure) as info:
        suite.run(make_series(np.random.default_rng(1).normal(0, 1, 60)), p)
    assert info.value.code == task_pb2.ERROR_CODE_INVALID_REQUEST


def test_delhi_rain_flags_few_months():
    o = suite.run(delhi("PRECTOTCORR"), plan("outliers")).outliers
    # Rain scatters far more in the monsoon than in the dry months. Judged
    # against the same month, only unusual rains stand out, a few percent.
    assert 0 < len(o.outliers) < 0.05 * 528
    assert o.highest.time.ToDatetime().strftime("%Y-%m") == "2003-07"
