"""Runs the series part of a TestPlan and builds the SeriesAnalysis document.

ANA-1 covers regression, Mann-Kendall and the agreement check. The other
series tests (ANA-3 and ANA-4) and the grid tests (ANA-2) are logged and
skipped, so their fields stay unset.
"""

from __future__ import annotations

import logging

import numpy as np

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.preprocess.v1 import data_pb2

from . import errors, kendall, regression
from .series import Monthly, month_timestamp, prepare

DEFAULT_ALPHA = 0.05
# Fewer effective values than this get a caveat.
FEW_EFFECTIVE = 10

ONE_TEST_SIGNIFICANT = (
    "Only one of the two tests finds the trend significant, so a few extreme months may be driving it. "
    "Treat it as borderline."
)
OPPOSITE_DIRECTIONS = (
    "The two tests find significant trends in opposite directions, so a few extreme months are probably "
    "driving the regression."
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

    ran: set[str] = set()
    skipped: list[str] = []
    for spec in plan.series:
        kind = spec.WhichOneof("test")
        if kind is None or kind in ran:
            continue
        if kind == "regression":
            result, notes = _regression(monthly, spec.regression, alpha)
            out.regression.CopyFrom(result)
            out.warnings.extend(notes)
        elif kind == "mann_kendall":
            out.mann_kendall.CopyFrom(_mann_kendall(monthly, spec.mann_kendall, alpha))
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


def _regression(
    m: Monthly, spec: analysis_pb2.Regression, alpha: float
) -> tuple[results_pb2.RegressionResult, list[str]]:
    fit = regression.fit(m.t, m.tested, alpha=alpha, correct_autocorrelation=spec.correct_autocorrelation)
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

    notes = []
    if spec.correct_autocorrelation and fit.n_effective < FEW_EFFECTIVE:
        notes.append(
            f"Neighboring months are so alike that the {fit.n} values count as only about "
            f"{max(1, round(fit.n_effective))} independent ones, so the test is cautious."
        )
    return result, notes


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
