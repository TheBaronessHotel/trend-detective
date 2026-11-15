"""The monthly series the tests run on, built from a preprocess Series.

Preprocess writes one step per time it has, so a month with no row at all is
absent rather than NaN. The tests want a regular calendar, so the values go
onto one slot per month from the first step to the last, NaN where missing.

Time is counted in months and turned into years as month / 12. A year is then
exactly 12 steps, and Seasonal Kendall's pairs within one calendar month are
whole years apart. The time origin is the mean time of the valid steps, so the
regression's intercept and slope come out uncorrelated.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from datetime import UTC, datetime

import numpy as np
from google.protobuf import timestamp_pb2

from trenddetective.analysis.v1 import analysis_pb2
from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2

from . import errors

# The least data the tests run on: a year's worth of values, spread over at
# least two years.
MIN_VALID = 12
MIN_YEARS = 2
# Records shorter than this get a caveat.
SHORT_RECORD_YEARS = 10
# More missing months than this share get a caveat.
MANY_MISSING = 0.2
# When the anomalies are computed here, a calendar month needs this many
# values to have an average of its own. Its anomalies are missing otherwise.
MIN_VALUES_PER_MONTH = 2

OWN_ANOMALIES = (
    "Each value was compared with the usual value for its month of the year, worked out from this series, "
    "because the prepared data came without anomalies."
)
RAW_VALUES = "The tests ran on the raw values, so the seasons are still in them."

_STEP_NAMES = {common_pb2.TIME_STEP_DAILY: "daily", common_pb2.TIME_STEP_WEEKLY: "weekly"}


@dataclass
class Monthly:
    """A series on a regular monthly calendar.

    Slot i is the month first_month + i, where a month index is
    year * 12 + month - 1.
    """

    first_month: int
    # The values as they are, NaN where missing.
    values: np.ndarray
    # The field the tests use, NaN where missing.
    tested: np.ndarray
    field: int
    # Units of tested.
    units: str
    # The mean month index of the valid tested steps.
    origin: float
    warnings: list[str] = field(default_factory=list)

    @property
    def size(self) -> int:
        return int(self.values.size)

    @property
    def n_valid(self) -> int:
        return int(np.isfinite(self.tested).sum())

    @property
    def span_years(self) -> float:
        """Years from the first valid month to the last, both counted."""
        valid = np.flatnonzero(np.isfinite(self.tested))
        return float(valid[-1] - valid[0] + 1) / 12

    @property
    def t(self) -> np.ndarray:
        """Years since the origin, for every slot."""
        return (self.first_month + np.arange(self.size) - self.origin) / 12.0

    @property
    def calendar_month(self) -> np.ndarray:
        """0 for January to 11 for December, for every slot."""
        return (self.first_month + np.arange(self.size)) % 12


def prepare(series: data_pb2.Series, field_choice: int) -> Monthly:
    """Puts the series on a monthly calendar and picks the field to test.

    Raises a TaskFailure when the series isn't monthly or has too little data.
    """
    n = len(series.times)
    if n == 0:
        raise errors.no_data("The prepared data is empty.", "Series.times is empty")
    if len(series.values) != n:
        raise errors.invalid(
            "The prepared data is damaged.", f"Series has {n} times but {len(series.values)} values"
        )
    first, slots, size = regular_calendar(series.times, series.step)

    values = _on_calendar(series.values, slots, size)
    calendar = (first + np.arange(size)) % 12
    warnings: list[str] = []

    choice = field_choice or analysis_pb2.SERIES_FIELD_ANOMALIES
    units = series.units
    if choice == analysis_pb2.SERIES_FIELD_ANOMALIES:
        if len(series.anomalies) == n:
            tested = _on_calendar(series.anomalies, slots, size)
            units = _anomaly_units(series)
        elif len(series.anomalies) == 0:
            tested = own_anomalies(values, calendar)
            warnings.append(OWN_ANOMALIES)
        else:
            raise errors.invalid(
                "The prepared data is damaged.", f"Series has {n} times but {len(series.anomalies)} anomalies"
            )
    elif choice == analysis_pb2.SERIES_FIELD_VALUES:
        tested = values.copy()
        warnings.append(RAW_VALUES)
    else:
        raise errors.invalid("The job asked for an unknown kind of series.", f"TestPlan.field is {choice}")

    valid = np.flatnonzero(np.isfinite(tested))
    span_months = int(valid[-1] - valid[0] + 1) if valid.size else 0
    if valid.size < MIN_VALID or span_months < MIN_YEARS * 12:
        raise errors.no_data(
            f"There's too little data for a trend test. It needs at least {MIN_VALID} monthly values "
            f"spread over {MIN_YEARS} years.",
            f"{valid.size} valid values over {span_months} months",
        )

    years = span_months / 12
    if years < SHORT_RECORD_YEARS:
        warnings.append(f"Only {_years_text(years)} years of data. A trend in a record this short is uncertain.")
    missing = 1 - valid.size / size
    if missing > MANY_MISSING:
        warnings.append(f"{missing:.0%} of the months have no value.")

    return Monthly(
        first_month=first,
        values=values,
        tested=tested,
        field=choice,
        units=units,
        origin=float(first + valid.mean()),
        warnings=warnings,
    )


def regular_calendar(times, step: int) -> tuple[int, np.ndarray, int]:
    """Where a list of monthly timestamps goes on a regular calendar: the
    month index of the first, each time's slot, and the number of slots.

    Raises a TaskFailure when the times aren't monthly.
    """
    if step not in (common_pb2.TIME_STEP_MONTHLY, common_pb2.TIME_STEP_UNSPECIFIED):
        name = _STEP_NAMES.get(step, "not monthly")
        raise errors.invalid(
            f"The trend tests need monthly values, and this series is {name}.",
            f"step is {common_pb2.TimeStep.Name(step)}; monthly_mean has to run in preprocess first",
        )
    months = np.array([_month_index(ts) for ts in times], dtype="int64")
    if np.any(np.diff(months) <= 0):
        raise errors.invalid(
            "The trend tests need one value per month, and this series has more.",
            "the times aren't one strictly increasing step per month",
        )
    first = int(months[0])
    slots = months - first
    return first, slots, int(slots[-1]) + 1


def own_anomalies(values: np.ndarray, calendar: np.ndarray) -> np.ndarray:
    """Each value minus its calendar month's normal, for values on a regular
    monthly calendar. See own_anomalies_cells."""
    return own_anomalies_cells(values[:, None], calendar)[:, 0]


def own_anomalies_cells(values: np.ndarray, calendar: np.ndarray) -> np.ndarray:
    """Anomalies for each column of values (time, cell) on a regular monthly
    calendar, each column with normals of its own.

    The normals come from a fit with one constant per calendar month and a
    straight-line trend, taken at the mean time, the same as preprocess's
    anomaly step. A plain mean of each calendar month would take in the
    trend's rise within the year and leave only a staircase from year to year,
    a trend 2% low over 7 years: see Validation in the README. The normal of
    month m is its mean minus b (mean time of month m - mean time), with b the
    trend within the months. A month seen in fewer than MIN_VALUES_PER_MONTH
    years has no normal, so no anomalies.
    """
    t = (np.arange(values.shape[0]) / 12.0)[:, None]
    ok = np.isfinite(values)
    month_of = calendar.astype("int64")
    count = np.stack([(ok & (month_of == m)[:, None]).sum(axis=0) for m in range(12)])
    has_normal = count >= MIN_VALUES_PER_MONTH  # (month, cell)
    use = ok & has_normal[month_of]
    w = use.astype("float64")
    y = np.where(use, values, 0.0)
    t_month = np.full(count.shape, np.nan)
    y_month = np.full(count.shape, np.nan)
    sxy = np.zeros(values.shape[1])
    sxx = np.zeros(values.shape[1])
    with np.errstate(invalid="ignore", divide="ignore"):
        for m in range(12):
            sel = month_of == m
            wm, ym, tm = w[sel], y[sel], t[sel]
            n = wm.sum(axis=0)
            t_month[m] = (wm * tm).sum(axis=0) / n
            y_month[m] = (wm * ym).sum(axis=0) / n
            dt = np.where(wm > 0, tm - t_month[m], 0.0)
            dy = np.where(wm > 0, ym - y_month[m], 0.0)
            sxy += np.nan_to_num((dt * dy).sum(axis=0))
            sxx += np.nan_to_num((dt * dt).sum(axis=0))
        trend = np.where(sxx > 0, sxy / np.where(sxx > 0, sxx, 1.0), 0.0)
        t_all = (w * t).sum(axis=0) / w.sum(axis=0)
        normal = y_month - trend * (t_month - t_all)
    return np.where(has_normal[month_of], values - normal[month_of], np.nan)


def month_timestamp(month_index: float) -> timestamp_pb2.Timestamp:
    """The instant a fractional month index stands for. Month k starts on its
    1st at 00:00 UTC, and the fraction is the share of that month gone by."""
    k = math.floor(month_index)
    year, month0 = divmod(k, 12)
    next_year, next_month0 = divmod(k + 1, 12)
    start = datetime(year, month0 + 1, 1, tzinfo=UTC)
    end = datetime(next_year, next_month0 + 1, 1, tzinfo=UTC)
    ts = timestamp_pb2.Timestamp()
    ts.FromDatetime(start + (end - start) * (month_index - k))
    return ts


def _years_text(years: float) -> str:
    """A length in years the way the web app's verdict writes one under 10: to
    a tenth, halves rounded up, so 117 months read 9.8 rather than 10, and a
    whole number without its decimal."""
    return f"{math.floor(years * 10 + 0.5) / 10:g}"


def _month_index(ts: timestamp_pb2.Timestamp) -> int:
    dt = ts.ToDatetime()
    return dt.year * 12 + dt.month - 1


def _on_calendar(seq, slots: np.ndarray, size: int) -> np.ndarray:
    out = np.full(size, np.nan)
    out[slots] = np.asarray(seq, dtype="float64")
    out[~np.isfinite(out)] = np.nan
    return out


def _anomaly_units(series: data_pb2.Series) -> str:
    if series.anomaly_kind == data_pb2.ANOMALY_KIND_PERCENT:
        return "%"
    if series.anomaly_kind == data_pb2.ANOMALY_KIND_STANDARDIZED:
        return "standard deviations"
    return series.units
