"""Turns a column of dates and a column of numbers into a point series.

Shared by the CSV decoders, which differ only in how they find those columns.
"""

from __future__ import annotations

import numpy as np
import pandas as pd
import xarray as xr

from trenddetective.common.v1 import common_pb2

from .. import canonical, errors


def parse_times(raw: pd.Series, file_name: str) -> np.ndarray:
    # Giovanni writes some timestamps with a space instead of "T".
    text = raw.astype(str).str.strip().str.replace(" ", "T", n=1, regex=False)
    times = pd.to_datetime(text, errors="coerce", utc=True, format="mixed")
    if times.isna().any():
        bad = raw[times.isna()].iloc[0]
        raise errors.unreadable(
            "A date in the file couldn't be read.", f"{file_name}: bad time {bad!r}"
        )
    return times.dt.tz_localize(None).values.astype("datetime64[ns]")


def parse_values(raw: pd.Series, fill_values: tuple[float, ...] = ()) -> np.ndarray:
    values = pd.to_numeric(raw.astype(str).str.strip().replace("", np.nan), errors="coerce")
    values = values.to_numpy(dtype="float64")
    for fill in fill_values:
        values[values == fill] = np.nan
    return values


def to_dataset(times: np.ndarray, values: np.ndarray, file_name: str) -> tuple[xr.Dataset, int]:
    if len(times) == 0:
        raise errors.no_data("The file has no rows of data.", f"{file_name}: empty")
    arr = xr.DataArray(values, dims=("time",), coords={"time": times})
    ds = canonical.combine([canonical.make_dataset(arr)])
    step = canonical.infer_step(ds["time"].values)
    if step == common_pb2.TIME_STEP_MONTHLY:
        ds = canonical.combine([canonical.snap_to_month_start(ds)])
    return ds, step
