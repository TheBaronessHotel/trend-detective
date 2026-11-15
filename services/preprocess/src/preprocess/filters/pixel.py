"""Steps that change the values themselves, pixel by pixel, in chain order.

Each takes and returns the canonical Dataset. A value a step drops becomes NaN
with the step's MissingReason. Values that were already missing keep theirs.
"""

from __future__ import annotations

import numpy as np
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from .. import canonical
from ..canonical import REASON, VALUE, Decoded

# A daily sample covers one day, a weekly one seven.
_DAYS_PER_SAMPLE = {common_pb2.TIME_STEP_DAILY: 1, common_pb2.TIME_STEP_WEEKLY: 7}


def _drop(ds: xr.Dataset, mask: xr.DataArray, reason: int) -> xr.Dataset:
    """Makes the values under mask missing with reason, if they weren't already."""
    newly = mask & ~np.isnan(ds[VALUE])
    ds = ds.copy()
    ds[VALUE] = ds[VALUE].where(~newly)
    ds[REASON] = xr.where(newly, reason, ds[REASON]).astype("int8")
    return ds


def qc_range(dec: Decoded, op: preprocess_pb2.QcRange) -> None:
    v = dec.data[VALUE]
    dec.data = _drop(dec.data, (v < op.min) | (v > op.max), data_pb2.MISSING_REASON_QC_FAILED)


def linear(dec: Decoded, op: preprocess_pb2.Linear) -> None:
    # A scale of exactly 0 would wipe every value, so an unset scale means 1.
    scale = op.scale if op.scale != 0 else 1.0
    ds = dec.data.copy()
    ds[VALUE] = ds[VALUE] * scale + op.offset
    dec.data = ds
    if op.units:
        dec.units = op.units


def log10(dec: Decoded, _op: preprocess_pb2.Log10) -> None:
    ds = _drop(dec.data, dec.data[VALUE] <= 0, data_pb2.MISSING_REASON_NOT_POSITIVE)
    with np.errstate(invalid="ignore", divide="ignore"):
        ds[VALUE] = np.log10(ds[VALUE])
    dec.data = ds
    dec.units = f"log₁₀({dec.units})" if dec.units else "log₁₀"


def monthly_mean(dec: Decoded, op: preprocess_pb2.MonthlyMean) -> None:
    """Daily or weekly values become the mean of each month's valid samples.

    A month counts only with at least min_valid_days of valid data, where a
    weekly sample counts as 7 days. A month without enough becomes
    TOO_FEW_DAYS. Monthly data passes through unchanged.
    """
    if dec.step not in _DAYS_PER_SAMPLE:
        dec.notes.append("monthly_mean skipped: the data is already monthly")
        return
    days = _DAYS_PER_SAMPLE[dec.step]
    v = dec.data[VALUE]
    mean = v.resample(time="MS").mean()
    valid_days = v.notnull().resample(time="MS").sum() * days
    enough = valid_days >= max(op.min_valid_days, 1)

    ds = xr.Dataset({VALUE: mean.where(enough)})
    ds[REASON] = xr.where(enough, 0, data_pb2.MISSING_REASON_TOO_FEW_DAYS).astype("int8")
    # Helpers, like surface pressure, become monthly means too, so a later
    # step can still use them.
    for name in dec.data.data_vars:
        if str(name).startswith(canonical.HELPER_PREFIX):
            ds[name] = dec.data[name].resample(time="MS").mean()
    dec.data = canonical.normalize_axes(ds)
    dec.step = common_pb2.TIME_STEP_MONTHLY


def coarsen(dec: Decoded, op: preprocess_pb2.Coarsen) -> None:
    """Averages blocks of factor × factor pixels. Point data has no pixels to
    average, so it passes through."""
    if not dec.gridded or op.factor <= 1:
        if not dec.gridded:
            dec.notes.append("coarsen skipped: the data is a single point")
        return
    v = dec.data[VALUE].coarsen(lat=op.factor, lon=op.factor, boundary="pad").mean()
    # A block's own reason is lost in the average. A block with no valid pixel
    # is marked as fill.
    dec.data = canonical.make_dataset(v)
