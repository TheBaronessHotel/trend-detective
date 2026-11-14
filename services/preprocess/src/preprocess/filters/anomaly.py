"""Normals and anomalies.

An anomaly is a value's difference from its calendar month's normal. The
contract keeps both, so the anomaly step adds an ``anomaly`` layer and leaves
the values alone.

The same functions serve each pixel and the finished region series, so the
region's normals in Series.normals follow the same rules as the pixels':

- **Baseline.** The years the normals come from: the step's baseline, or the
  whole record, clipped to the years the data covers.
- **Enough years.** A calendar month's normal counts only if that month has
  data in at least min_baseline_coverage (default 0.7) of the baseline years
  it appears in.
- **Method.** The mean of each calendar month, or a fit of ``harmonics``
  sine and cosine pairs (default 2), which is steadier for short records.
- **Trend.** Either way a straight-line trend is fitted along with the
  seasons, and the normals are the seasons at the baseline's mean time. A
  plain mean of each calendar month would take in the trend's rise within
  the year, because December comes later than January, and the anomalies
  would keep only a staircase from year to year: a trend comes out low by
  about 1 / Y^2 for Y years, 2% over 7, and a strong one leaves a sawtooth
  in the residuals. Analysis checks this against NOAA's published CO2
  growth. For the monthly means, the normal of month m is its mean minus
  b (mean time of month m - mean time), with b the trend within the months.
- **Kind.**
  - Absolute: value minus normal.
  - Percent: the difference as a percentage of the normal. A month whose
    normal is under NEAR_ZERO of that pixel's largest monthly normal has no
    percent anomaly, like desert rain. Never used for temperatures.
  - Standardized: the difference over the month's standard deviation. A month
    whose deviation is zero has none.

A month without a usable normal has no anomaly. For the region, that shows as
``usable: false`` in Series.normals, which explains every anomaly missing
where the value isn't.
"""

from __future__ import annotations

import math
import warnings
from dataclasses import dataclass

import numpy as np
import pandas as pd

from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from .. import errors
from ..decoders.common import norm_units

DEFAULT_MIN_BASELINE_COVERAGE = 0.7
DEFAULT_HARMONICS = 2
# A percent anomaly needs a normal of at least this share of the pixel's
# largest monthly normal.
NEAR_ZERO = 0.01

_TEMPERATURE_UNITS = {"°c", "k", "°f", "degf"}


@dataclass
class Normals:
    """Per calendar month (axis 0, January first) and per pixel (axis 1)."""

    mean: np.ndarray
    sd: np.ndarray
    years: np.ndarray
    usable: np.ndarray


def check(op: preprocess_pb2.Anomaly, units: str) -> None:
    """Refuses settings that can't make sense, before any work is done."""
    if op.kind == data_pb2.ANOMALY_KIND_PERCENT and norm_units(units) in _TEMPERATURE_UNITS:
        raise errors.invalid(
            "This variable can't be shown as a percent change.",
            f"percent anomaly on a temperature in {units!r}",
        )


def baseline_years(times: np.ndarray, op: preprocess_pb2.Anomaly) -> tuple[int, int]:
    years = pd.DatetimeIndex(times).year
    first, last = int(years.min()), int(years.max())
    if not op.HasField("baseline"):
        return first, last
    start = max(op.baseline.start_year or first, first)
    end = min(op.baseline.end_year or last, last)
    if start > end:
        raise errors.invalid(
            "The anomaly baseline is outside the years this data covers.",
            f"baseline {op.baseline.start_year}-{op.baseline.end_year}, data {first}-{last}",
        )
    return start, end


def normals(values: np.ndarray, times: np.ndarray, op: preprocess_pb2.Anomaly, base: tuple[int, int]) -> Normals:
    """The normal of each calendar month for each column of values (time, pixel)."""
    idx = pd.DatetimeIndex(times)
    months = np.asarray(idx.month)
    in_base = np.asarray((idx.year >= base[0]) & (idx.year <= base[1]))
    t = _years(times)
    p = values.shape[1]
    mean = np.full((12, p), np.nan)
    sd = np.full((12, p), np.nan)
    years = np.zeros((12, p), dtype=int)
    need = np.zeros(12, dtype=int)
    coverage = op.min_baseline_coverage or DEFAULT_MIN_BASELINE_COVERAGE

    with warnings.catch_warnings():
        warnings.simplefilter("ignore", RuntimeWarning)  # all-NaN columns
        for m in range(1, 13):
            sel = in_base & (months == m)
            x = values[sel]
            need[m - 1] = max(1, math.ceil(coverage * int(sel.sum()) - 1e-9))
            years[m - 1] = np.count_nonzero(~np.isnan(x), axis=0)
            if x.shape[0]:
                mean[m - 1] = np.nanmean(x, axis=0)
                sd[m - 1] = np.nanstd(x, axis=0, ddof=1) if x.shape[0] > 1 else np.nan

    if op.method == data_pb2.CLIMATOLOGY_METHOD_HARMONIC:
        mean, sd = _harmonic(values, months, in_base, t, op.harmonics or DEFAULT_HARMONICS, mean, sd)
    else:
        mean = mean - _rise_within_year(values, months, in_base, t)

    usable = (years >= need[:, None]) & ~np.isnan(mean)
    if op.kind == data_pb2.ANOMALY_KIND_PERCENT:
        with warnings.catch_warnings():
            warnings.simplefilter("ignore", RuntimeWarning)
            largest = np.nanmax(np.abs(mean), axis=0)
        usable &= np.abs(mean) >= NEAR_ZERO * largest
        usable &= mean != 0
    if op.kind == data_pb2.ANOMALY_KIND_STANDARDIZED:
        # Zero up to rounding: identical values can leave a spread of 1e-15.
        usable &= np.nan_to_num(sd) > 1e-9 * np.maximum(np.nan_to_num(np.abs(mean)), 1e-12)
    return Normals(mean=mean, sd=sd, years=years, usable=usable)


def _years(times: np.ndarray) -> np.ndarray:
    """Time in years of 365.25 days since 1970."""
    seconds = np.asarray(times).astype("datetime64[s]").astype("int64")
    return seconds / (365.25 * 86400)


def _rise_within_year(values, months, in_base, t):
    """For each calendar month (axis 0) and pixel (axis 1), how far a
    straight-line trend has moved that month's mean: b (t_m - t_all), where
    t_m is the mean time of the month's baseline values, t_all that of all of
    them, and b the slope fitted within the months, the trend of a fit with
    one constant per calendar month. Subtracted from the monthly means, it
    leaves the normals at the baseline's mean time. 0 where there's no data
    or no trend to fit."""
    ok = in_base[:, None] & ~np.isnan(values)
    w = ok.astype("float64")
    y = np.where(ok, values, 0.0)
    tt = np.broadcast_to(t[:, None], values.shape)
    sxy = np.zeros(values.shape[1])
    sxx = np.zeros(values.shape[1])
    t_month = np.full((12, values.shape[1]), np.nan)
    with np.errstate(invalid="ignore", divide="ignore"):
        t_all = (w * tt).sum(axis=0) / w.sum(axis=0)
        for m in range(12):
            sel = months == m + 1
            wm, ym, tm = w[sel], y[sel], tt[sel]
            n = wm.sum(axis=0)
            t_month[m] = (wm * tm).sum(axis=0) / n
            y_month = (wm * ym).sum(axis=0) / n
            dt = np.where(wm > 0, tm - t_month[m], 0.0)
            dy = np.where(wm > 0, ym - y_month, 0.0)
            sxy += np.nan_to_num((dt * dy).sum(axis=0))
            sxx += np.nan_to_num((dt * dt).sum(axis=0))
        b = np.where(sxx > 0, sxy / np.where(sxx > 0, sxx, 1.0), 0.0)
        return np.nan_to_num(b * (t_month - t_all))


def _harmonic(values, months, in_base, t, harmonics, mean, sd):
    """Replaces the monthly means with a smooth fit of the seasonal cycle and a
    straight-line trend, evaluated at the mean time of the baseline values,
    and the deviations with those of the residuals from the fit."""
    phase = 2 * np.pi * (months - 1) / 12.0
    cols = [np.ones_like(phase)]
    for k in range(1, harmonics + 1):
        cols += [np.cos(k * phase), np.sin(k * phase)]
    design = np.stack(cols, axis=1)  # (time, terms)
    month_phase = 2 * np.pi * np.arange(12) / 12.0
    month_design = np.stack(
        [np.ones(12)] + [f(k * month_phase) for k in range(1, harmonics + 1) for f in (np.cos, np.sin)], axis=1
    )
    fit_mean = np.full_like(mean, np.nan)
    fit_sd = np.full_like(sd, np.nan)
    for j in range(values.shape[1]):
        ok = in_base & ~np.isnan(values[:, j])
        # One more term than the cycle, for the trend.
        if ok.sum() < design.shape[1] + 2:
            continue
        trend = t - t[ok].mean()
        full = np.column_stack([design, trend])
        coef, *_ = np.linalg.lstsq(full[ok], values[ok, j], rcond=None)
        # The trend term is 0 at the mean time, so the normal is the cycle.
        fit_mean[:, j] = month_design @ coef[:-1]
        resid = values[:, j] - full @ coef
        for m in range(12):
            r = resid[ok & (months == m + 1)]
            fit_sd[m, j] = np.std(r, ddof=1) if r.size > 1 else np.nan
    return fit_mean, fit_sd


def anomalies(values: np.ndarray, times: np.ndarray, op: preprocess_pb2.Anomaly, n: Normals) -> np.ndarray:
    """Each value's anomaly, NaN where the value or its month's normal is missing."""
    m = np.asarray(pd.DatetimeIndex(times).month) - 1
    normal = n.mean[m]
    usable = n.usable[m]
    with np.errstate(invalid="ignore", divide="ignore"):
        if op.kind == data_pb2.ANOMALY_KIND_PERCENT:
            out = (values - normal) / np.abs(normal) * 100.0
        elif op.kind == data_pb2.ANOMALY_KIND_STANDARDIZED:
            out = (values - normal) / n.sd[m]
        else:
            out = values - normal
    return np.where(usable, out, np.nan)


def monthly_normals(n: Normals, column: int = 0) -> list[data_pb2.MonthlyNormal]:
    """One column of Normals as the contract's MonthlyNormal list."""
    return [
        data_pb2.MonthlyNormal(
            month=m + 1,
            mean=float(n.mean[m, column]),
            sd=float(n.sd[m, column]),
            years=int(n.years[m, column]),
            usable=bool(n.usable[m, column]),
        )
        for m in range(12)
    ]


def year_range(base: tuple[int, int]) -> common_pb2.YearRange:
    return common_pb2.YearRange(start_year=base[0], end_year=base[1])
