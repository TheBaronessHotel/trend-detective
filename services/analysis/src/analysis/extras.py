"""The extras from the science notes: the shuffle test, the trend triangle, a
trend for each calendar month, and the years to significance.

They test the same field as the regression and use the same least squares.
Where they need a p-value, they use the same autocorrelation correction as the
plan's regression, or the correction on when the plan has no regression.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

from . import regression
from .series import MIN_VALID, Monthly

# The shuffle test needs this many whole blocks. Fewer can't be put in enough
# orders: 5 blocks already have only 120.
MIN_BLOCKS = 5
# Fixed, so a repeat task draws the same shuffles and its result can be cached.
SHUFFLE_SEED = 0
# Shuffles are drawn this many at a time, to keep the index arrays small.
CHUNK = 1000
# A calendar month needs values in this many years for a trend of its own.
MIN_YEARS_PER_MONTH = 5
# Weatherhead et al. (1998): the factor for detecting a trend with 90%
# probability at the 5% level.
DETECTION_FACTOR = 3.3
# n* grows without bound as the autocorrelation nears 1, so it's clipped here.
MAX_PHI = 0.99


@dataclass(frozen=True)
class Shuffled:
    # Per year.
    observed: float
    p_value: float
    # Per year, one for each shuffle.
    slopes: np.ndarray


@dataclass(frozen=True)
class Triangle:
    start_years: list[int]
    end_years: list[int]
    # Per year, [start][end], NaN where there's no window.
    slopes: np.ndarray
    p_values: np.ndarray


@dataclass(frozen=True)
class MonthTrend:
    # 1 for January to 12 for December.
    month: int
    # Per year. NaN when the month has too few years.
    slope: float
    p_value: float


@dataclass(frozen=True)
class Detection:
    years_needed: float
    years_so_far: float
    noise_sd: float
    trend_per_year: float
    phi: float


def shuffle_test(t: np.ndarray, y: np.ndarray, *, permutations: int, block_length: int) -> Shuffled | None:
    """How often a shuffled series has a least-squares slope at least as steep
    as the real one.

    The series is cut into blocks of block_length steps from its first month,
    and the whole blocks change places while the times stay put. That keeps the
    autocorrelation within a block, and with 12-step blocks every value stays
    in its calendar month. A tail shorter than a block stays at the end.

    The p-value counts the real series as one of the shuffles, (k + 1) / (N + 1),
    so it is never 0. None when there are fewer than MIN_BLOCKS whole blocks.
    """
    if y.size // block_length < MIN_BLOCKS:
        return None
    rng = np.random.default_rng(SHUFFLE_SEED)
    observed = float(ols_slopes(t, y[None, :])[0])
    slopes = np.empty(permutations)
    for start in range(0, permutations, CHUNK):
        rows = min(CHUNK, permutations - start)
        slopes[start : start + rows] = ols_slopes(t, y[block_orders(y.size, block_length, rows, rng)])
    # Equal slopes count as extreme, with room for rounding.
    extreme = int(np.count_nonzero(np.abs(slopes) >= abs(observed) * (1 - 1e-9)))
    return Shuffled(observed=observed, p_value=(extreme + 1) / (permutations + 1), slopes=slopes)


def block_orders(size: int, block_length: int, rows: int, rng: np.random.Generator) -> np.ndarray:
    """Index arrays, one row per shuffle: the whole blocks in a random order,
    then the tail in place."""
    blocks = size // block_length
    order = rng.permuted(np.tile(np.arange(blocks), (rows, 1)), axis=1)
    idx = (order[:, :, None] * block_length + np.arange(block_length)).reshape(rows, blocks * block_length)
    tail = np.arange(blocks * block_length, size)
    return np.hstack([idx, np.tile(tail, (rows, 1))])


def ols_slopes(t: np.ndarray, y: np.ndarray) -> np.ndarray:
    """The least-squares slope of each row of y on t, skipping NaN."""
    w = np.isfinite(y)
    tw = np.where(w, t, 0.0)
    yw = np.where(w, y, 0.0)
    n = w.sum(axis=1)
    st, sy = tw.sum(axis=1), yw.sum(axis=1)
    sxx = (tw * tw).sum(axis=1) - st * st / n
    sxy = (tw * yw).sum(axis=1) - st * sy / n
    return sxy / sxx


def trend_triangle(m: Monthly, *, min_years: int, alpha: float, correct: bool) -> Triangle | None:
    """The trend of every window of whole calendar years at least min_years
    long, to show whether a trend depends on where the record starts and ends.

    Rows are start years, columns end years. A cell is NaN when its window is
    shorter than min_years or has fewer than MIN_VALID values. None when the
    record spans fewer than min_years calendar years.
    """
    year = (m.first_month + np.arange(m.size)) // 12
    valid = np.flatnonzero(np.isfinite(m.tested))
    first, last = int(year[valid[0]]), int(year[valid[-1]])
    if last - first + 1 < min_years:
        return None
    starts = list(range(first, last - min_years + 2))
    ends = list(range(first + min_years - 1, last + 1))
    slopes = np.full((len(starts), len(ends)), np.nan)
    p_values = np.full_like(slopes, np.nan)
    t = m.t
    for i, start in enumerate(starts):
        for j, end in enumerate(ends):
            if end - start + 1 < min_years:
                continue
            # Whole years in a row, so the window is a slice of the calendar
            # and lag-1 pairs stay real neighbors.
            window = (year >= start) & (year <= end)
            y = m.tested[window]
            if np.isfinite(y).sum() < MIN_VALID:
                continue
            fit = regression.fit(t[window], y, alpha=alpha, correct_autocorrelation=correct)
            slopes[i, j], p_values[i, j] = fit.slope, fit.p_value
    return Triangle(start_years=starts, end_years=ends, slopes=slopes, p_values=p_values)


def monthly_trends(m: Monthly, *, alpha: float, correct: bool) -> list[MonthTrend]:
    """A trend for each calendar month from its values across the years,
    January first. A month seen in fewer than MIN_YEARS_PER_MONTH years gets
    NaN."""
    calendar = m.calendar_month
    t = m.t
    out = []
    for month in range(12):
        sel = calendar == month
        y = m.tested[sel]
        if np.isfinite(y).sum() < MIN_YEARS_PER_MONTH:
            out.append(MonthTrend(month=month + 1, slope=math.nan, p_value=math.nan))
            continue
        # One value a year, so neighbors are a year apart.
        fit = regression.fit(t[sel], y, alpha=alpha, correct_autocorrelation=correct)
        out.append(MonthTrend(month=month + 1, slope=fit.slope, p_value=fit.p_value))
    return out


def years_to_significance(fit: regression.LinearTrend, years_so_far: float) -> Detection:
    """Weatherhead et al. (1998): the years of monthly data needed to detect
    the fitted trend with 90% probability,

        n* = [3.3 sd / |trend| * sqrt((1 + phi) / (1 - phi))] ^ (2/3),

    with sd the spread of the residuals, trend per year, and phi their lag-1
    autocorrelation. Infinite for a flat line.
    """
    phi = min(max(fit.lag1_autocorrelation, -MAX_PHI), MAX_PHI)
    if fit.slope == 0:
        needed = math.inf
    else:
        ratio = DETECTION_FACTOR * fit.residual_sd / abs(fit.slope) * math.sqrt((1 + phi) / (1 - phi))
        needed = ratio ** (2 / 3)
    return Detection(
        years_needed=needed,
        years_so_far=years_so_far,
        noise_sd=fit.residual_sd,
        trend_per_year=fit.slope,
        phi=fit.lag1_autocorrelation,
    )
