"""Fast trend tests on every cell of a prepared grid, with a Benjamini-Hochberg
correction across the cells (ANA-2).

The science notes keep the full suite for the region series and give each
cell only the fast tests, so a heatmap of thousands of cells stays quick:
least squares with the autocorrelation correction, and Mann-Kendall with
Sen's slope. Each cell's numbers are the ones the series code would give for
that cell alone, which the unit tests check. They're computed for all cells
at once instead of one by one.
"""

from __future__ import annotations

import numpy as np
from scipy import stats

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.preprocess.v1 import data_pb2

from . import errors, kendall
from .regression import MIN_N_EFF_FOR_TEST
from .series import MIN_VALID, MIN_YEARS, own_anomalies_cells, regular_calendar

DEFAULT_FDR_Q = 0.05
# Sen's slope holds every pair of values for a chunk of cells at once, about
# this many in all.
PAIRS_PER_CHUNK = 2_000_000
# The S statistic walks the cells in chunks this wide.
CELLS_PER_CHUNK = 2048


def run(
    grid: data_pb2.Grid,
    values: np.ndarray,
    anomalies: np.ndarray | None,
    plan: analysis_pb2.TestPlan,
    *,
    alpha: float,
    units: str,
) -> results_pb2.GridAnalysis:
    """Runs the plan's grid tests. values and anomalies are (time, rows, cols),
    anomalies None when preprocess made none."""
    q = plan.fdr_q or DEFAULT_FDR_Q
    if not 0 < q < 1:
        raise errors.invalid("The false discovery rate has to be between 0 and 1.", f"TestPlan.fdr_q is {q}")
    first, slots, size = regular_calendar(grid.times, grid.step)
    cells = grid.rows * grid.cols
    calendar = (first + np.arange(size)) % 12
    t = np.arange(size) / 12.0

    def on_calendar(cube: np.ndarray) -> np.ndarray:
        out = np.full((size, cells), np.nan)
        out[slots] = cube.reshape(len(slots), cells)
        out[~np.isfinite(out)] = np.nan
        return out

    field = plan.field or analysis_pb2.SERIES_FIELD_ANOMALIES
    if field == analysis_pb2.SERIES_FIELD_VALUES:
        y = on_calendar(values)
    elif anomalies is not None:
        y = on_calendar(anomalies)
    else:
        y = own_anomalies_cells(on_calendar(values), calendar)

    # The same least data as for the series: 12 values over two years.
    valid = np.isfinite(y)
    valid_steps = valid.sum(axis=0)
    first_valid = valid.argmax(axis=0)
    last_valid = size - 1 - valid[::-1].argmax(axis=0)
    enough = (valid_steps >= MIN_VALID) & (last_valid - first_valid + 1 >= MIN_YEARS * 12)

    out = results_pb2.GridAnalysis(
        variable_id=grid.variable_id,
        units=units,
        alpha=alpha,
        fdr_q=q,
        rows=grid.rows,
        cols=grid.cols,
        lats=list(grid.lats),
        lons=list(grid.lons),
        valid_steps=valid_steps.tolist(),
    )
    out.bbox.CopyFrom(grid.bbox)

    tested = y[:, enough]
    for spec in plan.grid:
        kind = spec.WhichOneof("test")
        if kind is None:
            continue
        test = analysis_pb2.GridTest()
        test.CopyFrom(spec)
        if kind == "regression":
            slope, p = regression_cells(t, tested, correct=spec.regression.correct_autocorrelation)
        else:
            variant = spec.mann_kendall.variant or kendall.HAMED_RAO
            if variant not in (kendall.ORIGINAL, kendall.HAMED_RAO, kendall.SEASONAL):
                raise errors.invalid(
                    "The job asked for an unknown Mann-Kendall test.", f"MannKendall.variant is {variant}"
                )
            test.mann_kendall.variant = variant
            slope, p = kendall_cells(t, tested, calendar, variant=variant, alpha=alpha)
        slopes = np.full(cells, np.nan)
        p_values = np.full(cells, np.nan)
        slopes[enough], p_values[enough] = slope * 10, p
        significant, threshold = benjamini_hochberg(p_values, q)
        out.layers.add(
            test=test,
            slopes_per_decade=slopes.tolist(),
            p_values=p_values.tolist(),
            significant=significant.tolist(),
            p_threshold=threshold,
        )
    return out


def benjamini_hochberg(p: np.ndarray, q: float) -> tuple[np.ndarray, float]:
    """Which p-values stay significant at false discovery rate q, and the
    largest of them, 0 when none does. NaN p-values don't count as tests."""
    tested = np.isfinite(p)
    significant = np.zeros(p.shape, dtype=bool)
    m = int(tested.sum())
    if m == 0:
        return significant, 0.0
    ordered = np.sort(p[tested])
    passing = np.flatnonzero(ordered <= q * np.arange(1, m + 1) / m)
    if passing.size == 0:
        return significant, 0.0
    threshold = float(ordered[passing[-1]])
    significant[tested] = p[tested] <= threshold
    return significant, threshold


def regression_cells(t: np.ndarray, y: np.ndarray, *, correct: bool) -> tuple[np.ndarray, np.ndarray]:
    """The least-squares slope per year and its t-test p-value for each column
    of y (time, cell), NaN where missing, the same as regression.fit."""
    w = np.isfinite(y)
    n = w.sum(axis=0).astype("float64")
    tt = t[:, None]
    with np.errstate(invalid="ignore", divide="ignore"):
        t_mean = np.where(w, tt, 0.0).sum(axis=0) / n
        y_mean = np.where(w, y, 0.0).sum(axis=0) / n
        dt = np.where(w, tt - t_mean, 0.0)
        dy = np.where(w, y - y_mean, 0.0)
        sxx = (dt * dt).sum(axis=0)
        slope = (dt * dy).sum(axis=0) / sxx
        resid = np.where(w, dy - slope * dt, 0.0)
        sse = (resid * resid).sum(axis=0)
        s2 = sse / (n - 2)
        # Lag-1 autocorrelation over real neighbors, as regression.lag1_autocorrelation.
        pairs = w[:-1] & w[1:]
        count = pairs.sum(axis=0)
        products = np.where(pairs, resid[:-1] * resid[1:], 0.0).sum(axis=0)
        r1 = np.where((count > 0) & (sse > 0), products / count * (n - 1) / sse, 0.0)
        r1 = np.clip(r1, -1.0, 1.0)
        n_eff = np.where(correct & (r1 > 0), n * (1 - r1) / (1 + r1), n)
        n_test = np.maximum(n_eff, MIN_N_EFF_FOR_TEST)
        se = np.sqrt(s2 / sxx) * np.sqrt((n - 2) / (n_test - 2))
        p = 2 * stats.t.sf(np.abs(slope / se), n_test - 2)
    # A perfect fit: the slope is exact.
    p = np.where(se > 0, p, np.where(slope == 0, 1.0, 0.0))
    return slope, p


def kendall_cells(
    t: np.ndarray, y: np.ndarray, calendar: np.ndarray, *, variant: int, alpha: float
) -> tuple[np.ndarray, np.ndarray]:
    """Sen's slope per year and the Mann-Kendall p-value for each column of y
    (time, cell), NaN where missing, the same as kendall.mann_kendall."""
    cells = y.shape[1]
    if variant == kendall.SEASONAL:
        # Pairs only within a calendar month are few, so cell by cell is fast enough.
        slope, p = np.empty(cells), np.empty(cells)
        for c in range(cells):
            r = kendall.mann_kendall(y[:, c], t, calendar, variant=variant, alpha=alpha)
            slope[c], p[c] = r.slope, r.p_value
        return slope, p

    w = np.isfinite(y)
    n = w.sum(axis=0).astype("float64")
    s = _s_cells(y, w)
    var = (n * (n - 1) * (2 * n + 5) - _tie_term(y, w)) / 18
    slope = _sen_cells(t, y, w)
    if variant == kendall.HAMED_RAO:
        for c in range(cells):
            x, tv = y[w[:, c], c], t[w[:, c]]
            var[c] *= kendall.hamed_rao_factor(x - slope[c] * tv, alpha)
    with np.errstate(invalid="ignore", divide="ignore"):
        z = np.where((var > 0) & (s != 0), (s - np.sign(s)) / np.sqrt(var), 0.0)
    return slope, 2 * stats.norm.sf(np.abs(z))


def _s_cells(y: np.ndarray, w: np.ndarray) -> np.ndarray:
    """Kendall's S per column: the signs of every later value minus every
    earlier one, over pairs where both are there."""
    s = np.zeros(y.shape[1])
    for start in range(0, y.shape[1], CELLS_PER_CHUNK):
        cols = slice(start, start + CELLS_PER_CHUNK)
        yc, wc = np.where(w[:, cols], y[:, cols], 0.0), w[:, cols]
        for i in range(y.shape[0] - 1):
            both = wc[i + 1 :] & wc[i]
            s[cols] += np.where(both, np.sign(yc[i + 1 :] - yc[i]), 0.0).sum(axis=0)
    return s


def _tie_term(y: np.ndarray, w: np.ndarray) -> np.ndarray:
    """Sum of t (t - 1) (2t + 5) over each column's groups of t equal values,
    for the variance of S."""
    ordered = np.sort(np.where(w, y, np.inf), axis=0)
    same = (ordered[1:] == ordered[:-1]) & np.isfinite(ordered[1:])
    run = np.zeros(y.shape[1])
    total = np.zeros(y.shape[1])
    for row in same:
        group = run + 1
        total += np.where(~row & (run > 0), group * (group - 1) * (2 * group + 5), 0.0)
        run = np.where(row, run + 1, 0.0)
    group = run + 1
    total += np.where(run > 0, group * (group - 1) * (2 * group + 5), 0.0)
    return total


def _sen_cells(t: np.ndarray, y: np.ndarray, w: np.ndarray) -> np.ndarray:
    """Sen's slope per column: the median slope over pairs of values that are
    both there, at their real times."""
    steps = y.shape[0]
    pairs = steps * (steps - 1) // 2
    width = max(1, PAIRS_PER_CHUNK // max(pairs, 1))
    out = np.full(y.shape[1], np.nan)
    for start in range(0, y.shape[1], width):
        yc, wc = y[:, start : start + width], w[:, start : start + width]
        slopes = np.empty((pairs, yc.shape[1]))
        both = np.empty((pairs, yc.shape[1]), dtype=bool)
        # The pairs one lag apart are two slices of the series, which is much
        # quicker to copy than picking every pair out by index.
        row = 0
        for lag in range(1, steps):
            n = steps - lag
            slopes[row : row + n] = (yc[lag:] - yc[:-lag]) / (t[lag:] - t[:-lag])[:, None]
            both[row : row + n] = wc[lag:] & wc[:-lag]
            row += n
        # One row per cell, so each cell's pairs sit together in memory.
        slopes, both = np.ascontiguousarray(slopes.T), np.ascontiguousarray(both.T)
        complete = both.all(axis=1)
        # Cells without gaps take the median all together. Cells with gaps
        # take it over their own pairs, a partial sort of each.
        if complete.any():
            out[start + np.flatnonzero(complete)] = np.median(slopes[complete], axis=1)
        for c in np.flatnonzero(~complete):
            ok = both[c]
            if ok.any():
                out[start + c] = np.median(slopes[c, ok])
    return out
