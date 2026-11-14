"""Mann-Kendall trend tests with Sen's slope.

Three variants, matching pymannkendall, which the unit tests use as the
reference:

- Original: Mann (1945) and Kendall (1975), with the correction for ties.
- Hamed and Rao (1998): the variance of S is scaled up for the significant
  autocorrelation of the ranks of the detrended series.
- Seasonal Kendall (Hirsch, Slack and Smith 1982): S and its variance are
  summed over the 12 calendar months, each month compared only with itself.

Sen's slope is the median of the pairwise slopes: all pairs for the first two
variants, and pairs within one calendar month for the seasonal one. Pairs use
the real times, so a gap doesn't squeeze the time axis. Its confidence
interval follows Sen (1968) and Gilbert (1987), the method of
scipy.stats.theilslopes, using the variance of S of the test that ran. For
Hamed and Rao that variance includes the autocorrelation, so the interval
widens with it.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np
from scipy import stats

from trenddetective.analysis.v1 import analysis_pb2

ORIGINAL = analysis_pb2.MANN_KENDALL_VARIANT_ORIGINAL
HAMED_RAO = analysis_pb2.MANN_KENDALL_VARIANT_HAMED_RAO
SEASONAL = analysis_pb2.MANN_KENDALL_VARIANT_SEASONAL


@dataclass(frozen=True)
class KendallTest:
    variant: int
    # Values the test used.
    n: int
    s: float
    # Variance of S, after the Hamed and Rao correction for that variant.
    var_s: float
    z: float
    p_value: float
    tau: float
    # Sen's slope and its confidence interval at 1 - alpha, per unit of t.
    slope: float
    slope_ci_low: float
    slope_ci_high: float


def mann_kendall(
    y: np.ndarray, t: np.ndarray, calendar_month: np.ndarray, *, variant: int, alpha: float
) -> KendallTest:
    """Runs one variant on y, which is NaN where missing. t holds the times of
    every slot in years, and calendar_month the month of every slot, 0 to 11."""
    if variant == SEASONAL:
        return _seasonal(y, t, calendar_month, alpha)
    if variant not in (ORIGINAL, HAMED_RAO):
        raise ValueError(f"unknown Mann-Kendall variant {variant}")

    valid = np.isfinite(y)
    x, tv = y[valid], t[valid]
    n = int(x.size)
    if n < 3:
        raise ValueError(f"Mann-Kendall needs at least 3 values, got {n}")
    s, var_s = s_statistic(x)
    slopes = np.sort(pairwise_slopes(x, tv))
    slope = float(np.median(slopes))
    if variant == HAMED_RAO:
        var_s *= hamed_rao_factor(x - slope * tv, alpha)
    z = z_score(s, var_s)
    low, high = sen_interval(slopes, var_s, alpha)
    return KendallTest(
        variant=variant,
        n=n,
        s=s,
        var_s=var_s,
        z=z,
        p_value=float(2 * stats.norm.sf(abs(z))),
        tau=s / (n * (n - 1) / 2),
        slope=slope,
        slope_ci_low=low,
        slope_ci_high=high,
    )


def _seasonal(y: np.ndarray, t: np.ndarray, calendar_month: np.ndarray, alpha: float) -> KendallTest:
    s = var_s = pairs = 0.0
    n = 0
    slopes = []
    for month in range(12):
        sel = (calendar_month == month) & np.isfinite(y)
        x, tv = y[sel], t[sel]
        n += int(x.size)
        if x.size < 2:
            continue
        s_m, var_m = s_statistic(x)
        s += s_m
        var_s += var_m
        pairs += x.size * (x.size - 1) / 2
        slopes.append(pairwise_slopes(x, tv))
    if not slopes:
        raise ValueError("Seasonal Kendall needs some calendar month with values in two years")
    all_slopes = np.sort(np.concatenate(slopes))
    z = z_score(s, var_s)
    low, high = sen_interval(all_slopes, var_s, alpha)
    return KendallTest(
        variant=SEASONAL,
        n=n,
        s=s,
        var_s=var_s,
        z=z,
        p_value=float(2 * stats.norm.sf(abs(z))),
        tau=s / pairs,
        slope=float(np.median(all_slopes)),
        slope_ci_low=low,
        slope_ci_high=high,
    )


def s_statistic(x: np.ndarray) -> tuple[float, float]:
    """Kendall's S for values in time order, and its variance with the
    correction for ties."""
    n = x.size
    i, j = np.triu_indices(n, 1)
    s = float(np.sign(x[j] - x[i]).sum())
    _, counts = np.unique(x, return_counts=True)
    ties = counts[counts > 1].astype("float64")
    var = (n * (n - 1) * (2 * n + 5) - float(np.sum(ties * (ties - 1) * (2 * ties + 5)))) / 18
    return s, var


def pairwise_slopes(x: np.ndarray, t: np.ndarray) -> np.ndarray:
    """The slope between every pair of values, for strictly increasing t."""
    i, j = np.triu_indices(x.size, 1)
    return (x[j] - x[i]) / (t[j] - t[i])


def z_score(s: float, var_s: float) -> float:
    """S with the continuity correction, in standard deviations."""
    if var_s <= 0 or s == 0:
        return 0.0
    return (s - math.copysign(1.0, s)) / math.sqrt(var_s)


def hamed_rao_factor(detrended: np.ndarray, alpha: float) -> float:
    """n / n*, the factor Hamed and Rao (1998) scale the variance of S by.

    It sums the autocorrelations of the ranks of the detrended series at every
    lag, keeping only those outside the 1 - alpha band around zero. As in
    pymannkendall, the series is taken as it comes, without its gaps.
    """
    n = detrended.size
    if n < 3:
        return 1.0
    acf = _acf(stats.rankdata(detrended))
    if acf is None:
        return 1.0
    lags = np.arange(1, n, dtype="float64")
    rho = acf[1:]
    keep = np.abs(rho) > stats.norm.ppf(1 - alpha / 2) / math.sqrt(n)
    total = float(np.sum(((n - lags) * (n - lags - 1) * (n - lags - 2) * rho)[keep]))
    factor = 1 + 2 * total / (n * (n - 1) * (n - 2))
    # Strong negative autocorrelation could push the factor to zero or below,
    # which leaves no variance to test with. The plain test is used then.
    return factor if factor > 0 else 1.0


def sen_interval(sorted_slopes: np.ndarray, var_s: float, alpha: float) -> tuple[float, float]:
    """Confidence interval for Sen's slope at 1 - alpha: the slopes ranked
    (N - C) / 2 and (N + C) / 2 + 1 of N, with C = z * sqrt(var S), rounded the
    way scipy.stats.theilslopes rounds them."""
    count = sorted_slopes.size
    if count == 0 or not var_s > 0:
        return math.nan, math.nan
    c = float(stats.norm.ppf(1 - alpha / 2)) * math.sqrt(var_s)
    lower = max(int(np.round((count - c) / 2)) - 1, 0)
    upper = min(int(np.round((count + c) / 2)), count - 1)
    return float(sorted_slopes[lower]), float(sorted_slopes[upper])


def _acf(x: np.ndarray) -> np.ndarray | None:
    """Autocorrelation at every lag, with the biased estimate pymannkendall
    and statsmodels use. None when x doesn't vary."""
    d = x - x.mean()
    acov = np.correlate(d, d, "full")[x.size - 1 :] / x.size
    if acov[0] <= 0:
        return None
    return acov / acov[0]
