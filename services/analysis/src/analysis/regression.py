"""Least-squares trend line with a t-test on the slope.

With the autocorrelation correction, the test uses the effective sample size
of Santer et al. (2000), n_eff = n (1 - r1) / (1 + r1), where r1 is the lag-1
autocorrelation of the residuals. The standard errors grow by
sqrt((n - 2) / (n_eff - 2)) and the t distribution has n_eff - 2 degrees of
freedom. Only positive r1 is corrected, as in Santer et al.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np
from scipy import stats

# The t-test needs a degree of freedom, so n_eff is floored at 3 for the test.
# The reported n_eff isn't floored.
MIN_N_EFF_FOR_TEST = 3.0


@dataclass(frozen=True)
class LinearTrend:
    n: int
    n_effective: float
    # Per year.
    slope: float
    slope_stderr: float
    # The line's value at t = 0.
    intercept: float
    intercept_stderr: float
    residual_sd: float
    lag1_autocorrelation: float
    r_squared: float
    p_value: float
    # Confidence interval for the slope at 1 - alpha, per year.
    ci_low: float
    ci_high: float


def fit(t: np.ndarray, y: np.ndarray, *, alpha: float, correct_autocorrelation: bool) -> LinearTrend:
    """Fits y = intercept + slope * t.

    t is in years for every slot of a regular calendar, and y is NaN where a
    value is missing, so lag-1 pairs are real neighbors.
    """
    valid = np.isfinite(y)
    n = int(valid.sum())
    if n < 3:
        raise ValueError(f"a trend line needs at least 3 values, got {n}")
    tv, yv = t[valid], y[valid]
    t_mean, y_mean = float(tv.mean()), float(yv.mean())
    tc = tv - t_mean
    sxx = float(tc @ tc)
    slope = float(tc @ (yv - y_mean)) / sxx
    intercept = y_mean - slope * t_mean

    residuals = np.full(y.shape, np.nan)
    residuals[valid] = yv - (intercept + slope * tv)
    sse = float(residuals[valid] @ residuals[valid])
    sst = float((yv - y_mean) @ (yv - y_mean))
    s2 = sse / (n - 2)
    r1 = lag1_autocorrelation(residuals)

    n_eff = float(n)
    if correct_autocorrelation and r1 > 0:
        n_eff = n * (1 - r1) / (1 + r1)
    n_test = max(n_eff, MIN_N_EFF_FOR_TEST)
    inflate = math.sqrt((n - 2) / (n_test - 2))
    dof = n_test - 2

    slope_se = math.sqrt(s2 / sxx) * inflate
    intercept_se = math.sqrt(s2 * (1 / n + t_mean**2 / sxx)) * inflate
    if slope_se > 0:
        p = float(2 * stats.t.sf(abs(slope / slope_se), dof))
    else:
        # A perfect fit: the slope is exact.
        p = 1.0 if slope == 0 else 0.0
    half = float(stats.t.ppf(1 - alpha / 2, dof)) * slope_se

    return LinearTrend(
        n=n,
        n_effective=n_eff,
        slope=slope,
        slope_stderr=slope_se,
        intercept=intercept,
        intercept_stderr=intercept_se,
        residual_sd=math.sqrt(s2),
        lag1_autocorrelation=r1,
        r_squared=1 - sse / sst if sst > 0 else 0.0,
        p_value=p,
        ci_low=slope - half,
        ci_high=slope + half,
    )


def lag1_autocorrelation(x: np.ndarray) -> float:
    """Lag-1 autocorrelation of a series on a regular calendar, NaN where
    missing.

    Only neighbors that both have a value form a pair. Without gaps this is the
    usual estimate, the sum of the n - 1 lagged products over the sum of the n
    squares. With gaps the products are scaled up to n - 1 pairs.
    """
    valid = np.isfinite(x)
    n = int(valid.sum())
    if n < 3:
        return 0.0
    d = np.where(valid, x - x[valid].mean(), 0.0)
    pairs = valid[:-1] & valid[1:]
    count = int(pairs.sum())
    squares = float(d @ d)
    if count == 0 or squares == 0:
        return 0.0
    products = float(d[:-1][pairs] @ d[1:][pairs])
    return float(np.clip(products / count * (n - 1) / squares, -1.0, 1.0))
