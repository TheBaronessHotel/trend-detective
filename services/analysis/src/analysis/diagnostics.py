"""Diagnostics from the science notes: the Pettitt change-point test, checks on
the regression's residuals, and outlier months by robust z-score.

They look at the same field as the regression. Outliers are flagged, never
dropped: the trend tests always use every value.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np
from scipy import stats

from . import regression

# Iglewicz and Hoaglin (1993): 0.6745 (x - median) / MAD is a z-score for
# normal data, since the MAD of a normal distribution is 0.6745 sd.
MAD_SCALE = 0.6745
# When half the values are the median, the MAD is 0, and the mean absolute
# deviation stands in: it is sd * sqrt(2 / pi), so 1 / 0.797885 = 1.253314.
MEAN_AD_SCALE = 1.253314
# A calendar month needs values in this many years for a median and MAD of
# its own, the same as for its trend.
MIN_YEARS_PER_MONTH = 5


@dataclass(frozen=True)
class ChangePoint:
    # The slot of the first value after the change.
    index: int
    # Pettitt's K, the largest |U|.
    k: float
    p_value: float
    mean_before: float
    mean_after: float
    # Whether a step at the change point fits the values better than the
    # regression's straight line, both with two parameters.
    step_fits_better: bool


@dataclass(frozen=True)
class ResidualChecks:
    lag1_autocorrelation: float
    autocorrelation_ok: bool
    normality_p_value: float
    normality_ok: bool


def pettitt(y: np.ndarray, line_sse: float) -> ChangePoint:
    """Pettitt's (1979) test for one change in the level of a series, NaN where
    missing, compared with a straight line whose residual sum of squares is
    line_sse.

    U_t sums sign(x_i - x_j) over every i up to t and j after it. K is the
    largest |U_t|, the change comes after its t, and p is about
    2 exp(-6 K^2 / (n^3 + n^2)). Gaps are squeezed out, so the change point is
    the first valid value after the change.
    """
    valid = np.flatnonzero(np.isfinite(y))
    x = y[valid]
    n = x.size
    # U_t = U_{t-1} + sum_j sign(x_t - x_j): the pairs within the first t
    # values cancel out, leaving those that straddle the split.
    u = np.cumsum(np.sign(x[:, None] - x[None, :]).sum(axis=1))[:-1]
    split = int(np.argmax(np.abs(u)))
    k = float(abs(u[split]))
    before, after = x[: split + 1], x[split + 1 :]
    step_sse = float(((before - before.mean()) ** 2).sum() + ((after - after.mean()) ** 2).sum())
    return ChangePoint(
        index=int(valid[split + 1]),
        k=k,
        p_value=min(1.0, 2 * math.exp(-6 * k * k / (n**3 + n**2))),
        mean_before=float(before.mean()),
        mean_after=float(after.mean()),
        step_fits_better=step_sse < line_sse,
    )


def residual_checks(residuals: np.ndarray, fit: regression.LinearTrend, alpha: float) -> ResidualChecks:
    """Pass or warn for the regression's residuals, NaN where missing.

    - Autocorrelation warns when the lag-1 autocorrelation is outside the
      1 - alpha band for white noise, +-z / sqrt(n).
    - Normality warns when Shapiro-Wilk rejects normal residuals at alpha.
    """
    e = residuals[np.isfinite(residuals)]
    bound = float(stats.norm.ppf(1 - alpha / 2)) / math.sqrt(e.size)
    r1 = fit.lag1_autocorrelation
    normality_p = 1.0 if np.ptp(e) == 0 else float(stats.shapiro(e).pvalue)
    return ResidualChecks(
        lag1_autocorrelation=r1,
        autocorrelation_ok=abs(r1) <= bound,
        normality_p_value=normality_p,
        normality_ok=normality_p >= alpha,
    )


def seasonal_robust_z(y: np.ndarray, calendar_month: np.ndarray) -> np.ndarray:
    """The robust z-score of every value against the same calendar month in
    the other years, NaN where missing.

    Months of the year can scatter very differently, like monsoon and dry
    months for rain. One median and spread for the whole series would call
    every monsoon month unusual. A month's own spread can be tiny, though,
    like November rain that is nearly always 0, so a month's spread is never
    taken as smaller than the whole series' spread.

    A calendar month seen in fewer than MIN_YEARS_PER_MONTH years isn't
    scored. When no month has that many, the record is too short to compare
    months with themselves, and every value is scored against the whole
    series instead.
    """
    valid = np.isfinite(y)
    counts = {m: int((valid & (calendar_month == m)).sum()) for m in range(12)}
    if max(counts.values()) < MIN_YEARS_PER_MONTH:
        return robust_z(y)
    _, whole = _center_and_spread(y[valid])
    z = np.full(y.shape, np.nan)
    for month, count in counts.items():
        if count >= MIN_YEARS_PER_MONTH:
            sel = calendar_month == month
            z[sel] = robust_z(y[sel], min_spread=whole)
    return z


def robust_z(y: np.ndarray, min_spread: float = 0.0) -> np.ndarray:
    """Iglewicz and Hoaglin's (1993) modified z-score of every value, NaN where
    missing: 0.6745 (x - median) / MAD, which is (x - median) / spread with
    spread = MAD / 0.6745. The spread is at least min_spread, and z is 0 when
    there's no spread at all."""
    valid = np.isfinite(y)
    median, spread = _center_and_spread(y[valid])
    spread = max(spread, min_spread)
    z = np.full(y.shape, np.nan)
    z[valid] = (y[valid] - median) / spread if spread > 0 else 0.0
    return z


def _center_and_spread(x: np.ndarray) -> tuple[float, float]:
    """The median, and a robust spread in standard deviations of normal data:
    MAD / 0.6745, or, when the MAD is 0, the mean absolute deviation times
    1.253314."""
    median = float(np.median(x))
    mad = float(np.median(np.abs(x - median)))
    if mad > 0:
        return median, mad / MAD_SCALE
    return median, MEAN_AD_SCALE * float(np.mean(np.abs(x - median)))
