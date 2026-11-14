"""Runs the series part of a TestPlan and builds the SeriesAnalysis document.

Regression, Mann-Kendall and the agreement check came with ANA-1, the extras
with ANA-4, and the diagnostics (Pettitt, residual checks, outliers) with
ANA-3. The grid tests (ANA-2) are logged and skipped, so their field stays
unset.
"""

from __future__ import annotations

import logging

import numpy as np

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.preprocess.v1 import data_pb2

from . import diagnostics, errors, extras, kendall, regression
from .series import MIN_VALID, MIN_YEARS, Monthly, month_timestamp, prepare

DEFAULT_ALPHA = 0.05
# Fewer effective values than this get a caveat.
FEW_EFFECTIVE = 10
DEFAULT_PERMUTATIONS = 1000
MAX_PERMUTATIONS = 10_000
DEFAULT_BLOCK_LENGTH = 12
DEFAULT_MIN_YEARS = 10
DEFAULT_ROBUST_Z = 3.5

PASS = results_pb2.CHECK_STATUS_PASS
WARN = results_pb2.CHECK_STATUS_WARN

_MONTH_NAMES = (
    "January", "February", "March", "April", "May", "June",
    "July", "August", "September", "October", "November", "December",
)  # fmt: skip

NOT_BELL_SHAPED_MK = (
    "The scatter around the trend line isn't bell-shaped, so the Mann-Kendall test, which doesn't need it to be, "
    "is the safer guide."
)
NOT_BELL_SHAPED = "The scatter around the trend line isn't bell-shaped, so the regression's p-value is only rough."
UNCORRECTED_AUTOCORRELATION = (
    "Neighboring months are alike, and the regression doesn't allow for that, so its p-value comes out too small."
)

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
        elif kind == "pettitt":
            _pettitt(out, monthly, fit, alpha)
        elif kind == "residual_checks":
            _residual_checks(out, monthly, fit, alpha)
        elif kind == "outliers":
            _outliers(out, monthly, spec.outliers, fit, alpha, correct)
        else:
            skipped.append(kind)
            continue
        ran.add(kind)

    if out.HasField("regression") and out.HasField("mann_kendall"):
        out.agreement.CopyFrom(_agreement(out.regression, out.mann_kendall))
        out.warnings.extend(_disagreement(out.regression, out.mann_kendall))
    # After the loop, because the advice depends on whether Mann-Kendall ran.
    if out.HasField("residual_checks"):
        checks = out.residual_checks
        if checks.autocorrelation == WARN and not correct:
            out.warnings.append(UNCORRECTED_AUTOCORRELATION)
        if checks.normality == WARN:
            out.warnings.append(NOT_BELL_SHAPED_MK if out.HasField("mann_kendall") else NOT_BELL_SHAPED)

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


def _pettitt(out: results_pb2.SeriesAnalysis, m: Monthly, fit: regression.LinearTrend, alpha: float) -> None:
    # The line's residual sum of squares, to tell a step from a steady change.
    line_sse = fit.residual_sd**2 * (fit.n - 2)
    change = diagnostics.pettitt(m.tested, line_sse)
    significant = change.p_value < alpha
    out.pettitt.CopyFrom(
        results_pb2.PettittResult(
            p_value=change.p_value,
            significant=significant,
            mean_before=change.mean_before,
            mean_after=change.mean_after,
        )
    )
    out.pettitt.change_time.CopyFrom(month_timestamp(m.first_month + change.index))
    # A steady trend makes Pettitt significant too, at its middle. Only a step
    # that beats the straight line is worth a caveat.
    if significant and change.step_fits_better:
        out.warnings.append(
            f"The values jump around {_month_name(m.first_month + change.index)} instead of changing steadily. "
            "A sudden step, like a new sensor or a new dam, can look like a trend."
        )


def _residual_checks(out: results_pb2.SeriesAnalysis, m: Monthly, fit: regression.LinearTrend, alpha: float) -> None:
    residuals = m.tested - (fit.intercept + fit.slope * m.t)
    checks = diagnostics.residual_checks(residuals, fit, alpha)
    out.residual_checks.CopyFrom(
        results_pb2.ResidualChecksResult(
            autocorrelation=PASS if checks.autocorrelation_ok else WARN,
            lag1_autocorrelation=checks.lag1_autocorrelation,
            normality=PASS if checks.normality_ok else WARN,
            normality_p_value=checks.normality_p_value,
        )
    )


def _outliers(
    out: results_pb2.SeriesAnalysis,
    m: Monthly,
    spec: analysis_pb2.Outliers,
    fit: regression.LinearTrend,
    alpha: float,
    correct: bool,
) -> None:
    threshold = spec.robust_z_threshold or DEFAULT_ROBUST_Z
    if not threshold > 0:
        raise errors.invalid(
            "The outlier threshold has to be above 0.", f"Outliers.robust_z_threshold is {threshold}"
        )
    z = diagnostics.seasonal_robust_z(m.tested, m.calendar_month)
    flagged = np.flatnonzero(np.abs(np.nan_to_num(z)) > threshold)
    result = results_pb2.OutliersResult(robust_z_threshold=threshold)
    result.outliers.extend(_outlier(m, z, i) for i in flagged)
    # The months furthest above and below normal, with anomalies.
    result.highest.CopyFrom(_outlier(m, z, int(np.nanargmax(m.tested))))
    result.lowest.CopyFrom(_outlier(m, z, int(np.nanargmin(m.tested))))
    out.outliers.CopyFrom(result)

    # Outliers stay in every test. The caveat says when they decide the
    # regression's verdict, as single flood months did in the SeaWiFS paper.
    if flagged.size == 0:
        return
    kept = m.tested.copy()
    kept[flagged] = np.nan
    if np.isfinite(kept).sum() < MIN_VALID:
        return
    without = regression.fit(m.t, kept, alpha=alpha, correct_autocorrelation=correct)
    if (without.p_value < alpha) == (fit.p_value < alpha):
        return
    one = flagged.size == 1
    months = "one unusual month" if one else f"{flagged.size} unusual months"
    them = "it" if one else "them"
    if fit.p_value < alpha:
        out.warnings.append(f"The trend rests on {months}: without {them}, the trend isn't significant.")
    else:
        verb = "hides" if one else "hide"
        out.warnings.append(
            f"{months[0].upper()}{months[1:]} {verb} the trend: without {them}, the trend would be significant."
        )


def _outlier(m: Monthly, z: np.ndarray, index: int) -> results_pb2.Outlier:
    o = results_pb2.Outlier(value=float(m.tested[index]), robust_z=float(z[index]))
    o.time.CopyFrom(month_timestamp(m.first_month + index))
    return o


def _month_name(month_index: int) -> str:
    year, month0 = divmod(month_index, 12)
    return f"{_MONTH_NAMES[month0]} {year}"


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
