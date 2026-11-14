"""Runs the series part of a TestPlan and builds the SeriesAnalysis document.

Regression, Mann-Kendall and the agreement check came with ANA-1. The shuffle
test, the trend triangle, the monthly trends and the years to significance
come with ANA-4. The diagnostics (ANA-3) and the grid tests (ANA-2) are logged
and skipped, so their fields stay unset.
"""

from __future__ import annotations

import logging

import numpy as np

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.preprocess.v1 import data_pb2

from . import errors, extras, kendall, regression
from .series import MIN_YEARS, Monthly, month_timestamp, prepare

DEFAULT_ALPHA = 0.05
# Fewer effective values than this get a caveat.
FEW_EFFECTIVE = 10
DEFAULT_PERMUTATIONS = 1000
MAX_PERMUTATIONS = 10_000
DEFAULT_BLOCK_LENGTH = 12
DEFAULT_MIN_YEARS = 10

ONE_TEST_SIGNIFICANT = (
    "Only one of the two tests finds the trend significant, so a few extreme months may be driving it. "
    "Treat it as borderline."
)
OPPOSITE_DIRECTIONS = (
    "The two tests find significant trends in opposite directions, so a few extreme months are probably "
    "driving the regression."
)
MONTHS_BY_CHANCE = (
    "Each month of the year was tested on its own, and with twelve tests one or two can look significant "
    "just by chance."
)

log = logging.getLogger("analysis.suite")


def run(
    series: data_pb2.Series,
    plan: analysis_pb2.TestPlan,
    logger: logging.LoggerAdapter | logging.Logger = log,
) -> results_pb2.SeriesAnalysis:
    alpha = plan.alpha or DEFAULT_ALPHA
    if not 0 < alpha < 0.5:
        raise errors.invalid("The significance level has to be between 0 and 0.5.", f"TestPlan.alpha is {alpha}")
    monthly = prepare(series, plan.field)

    out = results_pb2.SeriesAnalysis(
        variable_id=series.variable_id,
        units=monthly.units,
        alpha=alpha,
        field=monthly.field,
        n_valid=monthly.n_valid,
        n_total=monthly.size,
    )
    out.warnings.extend(monthly.warnings)

    # One fit serves the regression result and the years to significance. Its
    # correction, also used by the trend triangle and the monthly trends,
    # follows the plan's regression, and is on when the plan has none.
    regressions = [s.regression for s in plan.series if s.WhichOneof("test") == "regression"]
    correct = regressions[0].correct_autocorrelation if regressions else True
    fit = regression.fit(monthly.t, monthly.tested, alpha=alpha, correct_autocorrelation=correct)

    ran: set[str] = set()
    skipped: list[str] = []
    for spec in plan.series:
        kind = spec.WhichOneof("test")
        if kind is None or kind in ran:
            continue
        if kind == "regression":
            out.regression.CopyFrom(_regression(monthly, fit, alpha))
            if correct and fit.n_effective < FEW_EFFECTIVE:
                out.warnings.append(
                    f"Neighboring months are so alike that the {fit.n} values count as only about "
                    f"{max(1, round(fit.n_effective))} independent ones, so the test is cautious."
                )
        elif kind == "mann_kendall":
            out.mann_kendall.CopyFrom(_mann_kendall(monthly, spec.mann_kendall, alpha))
        elif kind == "shuffle":
            _shuffle(out, monthly, spec.shuffle)
        elif kind == "trend_triangle":
            _trend_triangle(out, monthly, spec.trend_triangle, alpha, correct)
        elif kind == "monthly_trends":
            _monthly_trends(out, monthly, alpha, correct)
        elif kind == "years_to_significance":
            _years_to_significance(out, monthly, fit, alpha)
        else:
            skipped.append(kind)
            continue
        ran.add(kind)

    if out.HasField("regression") and out.HasField("mann_kendall"):
        out.agreement.CopyFrom(_agreement(out.regression, out.mann_kendall))
        out.warnings.extend(_disagreement(out.regression, out.mann_kendall))

    if skipped:
        logger.info("skipped tests that aren't built yet", extra={"fields": {"tests": ",".join(skipped)}})
    if plan.grid:
        logger.info("skipped grid tests, which aren't built yet", extra={"fields": {"tests": len(plan.grid)}})
    return out


def _regression(m: Monthly, fit: regression.LinearTrend, alpha: float) -> results_pb2.RegressionResult:
    result = results_pb2.RegressionResult(
        slope_per_decade=fit.slope * 10,
        slope_ci_low=fit.ci_low * 10,
        slope_ci_high=fit.ci_high * 10,
        p_value=fit.p_value,
        significant=fit.p_value < alpha,
        r_squared=fit.r_squared,
        n=fit.n,
        n_effective=fit.n_effective,
    )
    model = result.model
    model.time_origin.CopyFrom(month_timestamp(m.origin))
    model.intercept = fit.intercept
    model.intercept_stderr = fit.intercept_stderr
    model.slope = fit.slope
    model.slope_stderr = fit.slope_stderr
    model.residual_sd = fit.residual_sd
    model.lag1_autocorrelation = fit.lag1_autocorrelation
    return result


def _mann_kendall(m: Monthly, spec: analysis_pb2.MannKendall, alpha: float) -> results_pb2.MannKendallResult:
    variant = spec.variant or kendall.HAMED_RAO
    if variant not in (kendall.ORIGINAL, kendall.HAMED_RAO, kendall.SEASONAL):
        raise errors.invalid("The job asked for an unknown Mann-Kendall test.", f"MannKendall.variant is {variant}")
    # Every variant runs on the tested field, Seasonal Kendall too, although
    # the proto says it uses the raw values. It compares each calendar month
    # only with itself, and an anomaly is the value shifted or scaled by its
    # month's normal, which keeps the order within a month. So S, z and p are
    # the same either way, absolute anomalies give the same slope too, and
    # percent or standardized ones give it in the same units as the rest.
    try:
        test = kendall.mann_kendall(m.tested, m.t, m.calendar_month, variant=variant, alpha=alpha)
    except ValueError as err:
        raise errors.no_data(
            "There's too little data for the Mann-Kendall test. It needs some month of the year in at least "
            "two years.",
            str(err),
        ) from err
    return results_pb2.MannKendallResult(
        variant=variant,
        tau=test.tau,
        z=test.z,
        p_value=test.p_value,
        significant=test.p_value < alpha,
        sens_slope_per_decade=test.slope * 10,
        sens_ci_low=test.slope_ci_low * 10,
        sens_ci_high=test.slope_ci_high * 10,
        n=test.n,
    )


def _shuffle(out: results_pb2.SeriesAnalysis, m: Monthly, spec: analysis_pb2.Shuffle) -> None:
    permutations = spec.permutations or DEFAULT_PERMUTATIONS
    block_length = spec.block_length or DEFAULT_BLOCK_LENGTH
    if not 1 <= permutations <= MAX_PERMUTATIONS:
        raise errors.invalid(
            f"The shuffle test can run from 1 to {MAX_PERMUTATIONS:,} shuffles.",
            f"Shuffle.permutations is {permutations}",
        )
    if block_length < 1:
        raise errors.invalid("The shuffle test's blocks need at least one month.", f"Shuffle.block_length is {block_length}")
    result = extras.shuffle_test(m.t, m.tested, permutations=permutations, block_length=block_length)
    if result is None:
        out.warnings.append(
            f"The record is too short for the shuffle test, which needs at least "
            f"{extras.MIN_BLOCKS * block_length} months."
        )
        return
    out.shuffle.CopyFrom(
        results_pb2.ShuffleResult(
            permutations=permutations,
            block_length=block_length,
            observed_slope_per_decade=result.observed * 10,
            p_value=result.p_value,
            shuffled_slopes_per_decade=(result.slopes * 10).tolist(),
        )
    )


def _trend_triangle(
    out: results_pb2.SeriesAnalysis, m: Monthly, spec: analysis_pb2.TrendTriangle, alpha: float, correct: bool
) -> None:
    min_years = spec.min_years or DEFAULT_MIN_YEARS
    if min_years < MIN_YEARS:
        raise errors.invalid(
            f"The trend triangle's shortest window has to be at least {MIN_YEARS} years.",
            f"TrendTriangle.min_years is {min_years}",
        )
    triangle = extras.trend_triangle(m, min_years=min_years, alpha=alpha, correct=correct)
    if triangle is None:
        out.warnings.append(
            f"The record is shorter than the {min_years} years the trend triangle's shortest window needs."
        )
        return
    out.trend_triangle.CopyFrom(
        results_pb2.TrendTriangleResult(
            min_years=min_years,
            start_years=triangle.start_years,
            end_years=triangle.end_years,
            slopes_per_decade=(triangle.slopes * 10).ravel().tolist(),
            p_values=triangle.p_values.ravel().tolist(),
        )
    )


def _monthly_trends(out: results_pb2.SeriesAnalysis, m: Monthly, alpha: float, correct: bool) -> None:
    trends = extras.monthly_trends(m, alpha=alpha, correct=correct)
    for trend in trends:
        out.monthly_trends.add(
            month=trend.month,
            slope_per_decade=trend.slope * 10,
            p_value=trend.p_value,
            significant=trend.p_value < alpha,
        )
    # At alpha 0.05, 46% of series with no trend at all show at least one
    # significant month among 12 (1 - 0.95^12), but three or more only 2%.
    if 1 <= sum(t.p_value < alpha for t in trends) <= 2:
        out.warnings.append(MONTHS_BY_CHANCE)


def _years_to_significance(
    out: results_pb2.SeriesAnalysis, m: Monthly, fit: regression.LinearTrend, alpha: float
) -> None:
    detection = extras.years_to_significance(fit, m.span_years)
    out.years_to_significance.CopyFrom(
        results_pb2.YearsToSignificanceResult(
            years_needed=detection.years_needed,
            years_so_far=detection.years_so_far,
            noise_sd=detection.noise_sd,
            trend_per_year=detection.trend_per_year,
            phi=detection.phi,
            already_significant=fit.p_value < alpha,
        )
    )


def _agreement(reg: results_pb2.RegressionResult, mk: results_pb2.MannKendallResult) -> results_pb2.Agreement:
    return results_pb2.Agreement(
        same_direction=bool(np.sign(reg.slope_per_decade) == np.sign(mk.sens_slope_per_decade)),
        same_significance=reg.significant == mk.significant,
    )


def _disagreement(reg: results_pb2.RegressionResult, mk: results_pb2.MannKendallResult) -> list[str]:
    """A caveat when the tests tell different stories. Two tests that both find
    nothing don't, even if their tiny slopes point different ways."""
    if reg.significant != mk.significant:
        return [ONE_TEST_SIGNIFICANT]
    if reg.significant and np.sign(reg.slope_per_decade) != np.sign(mk.sens_slope_per_decade):
        return [OPPOSITE_DIRECTIONS]
    return []
