"""The in-memory form every decoder returns.

Decoders differ only in how they read a file. After decoding, the rest of
preprocess (the filter chain, aggregation, the writers) sees the same shape for
every source:

- an xarray Dataset with two variables over the same dims:
  - ``value``: float64, NaN where missing;
  - ``reason``: int8 MissingReason codes, meaningful only where value is NaN;
- dims ``(time,)`` for point series, or ``(time, lat, lon)`` for grids;
- ``time`` holds the start of each step in UTC, as naive datetime64;
- ``lat`` runs north first and ``lon`` west first, in -180 to 180.

After the filter chain, the Dataset can also hold ``anomaly``: float64 over the
same dims, the per-pixel anomaly. Values stay in ``value``.

Before the chain runs, two more things can appear, and the chain must remove
both:

- a ``level`` dim, between ``time`` and ``lat``: pressure levels in hPa, for
  profiles like AIRS methane. A column_average step collapses it.
- helper variables, named ``helper:<variable path>``, like AIRS surface
  pressure: inputs a filter step names, over ``(time, lat, lon)``.

Nothing outside preprocess sees this form. The documents other services read
are Series and Grid, written by output.py.
"""

from __future__ import annotations

from dataclasses import dataclass, field

import numpy as np
import pandas as pd
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from . import errors

VALUE = "value"
REASON = "reason"
ANOMALY = "anomaly"
HELPER_PREFIX = "helper:"


def helper_name(variable_path: str) -> str:
    """The Dataset name of a helper variable read from variable_path."""
    return HELPER_PREFIX + variable_path

FILL_VALUE = data_pb2.MISSING_REASON_FILL_VALUE


@dataclass
class Decoded:
    data: xr.Dataset
    units: str
    step: int = common_pb2.TIME_STEP_UNSPECIFIED
    # Set for single-point sources like POWER. Grids leave it unset.
    point: common_pb2.LatLon | None = None
    # Things a later step or the provenance log should know, like the units a
    # file declared when they differed from the task's.
    notes: list[str] = field(default_factory=list)
    # Set by the filter chain. The anomaly step's settings, with the baseline
    # it actually used, so aggregation can compute the region's normals the
    # same way. None without an anomaly step.
    anomaly: preprocess_pb2.Anomaly | None = None
    # Longest gap gap_fill_display fills, in steps. 0 means no display series.
    gap_fill_max: int = 0
    # The steps the chain ran, in order, for the log.
    applied: list[str] = field(default_factory=list)

    @property
    def anomalies(self) -> xr.DataArray | None:
        return self.data[ANOMALY] if ANOMALY in self.data else None

    @property
    def gridded(self) -> bool:
        return "lat" in self.data.dims and "lon" in self.data.dims

    @property
    def values(self) -> xr.DataArray:
        return self.data[VALUE]

    @property
    def reasons(self) -> xr.DataArray:
        return self.data[REASON]


def make_dataset(values: xr.DataArray, reasons: xr.DataArray | None = None) -> xr.Dataset:
    """Builds the canonical Dataset. Missing values without a reason get FILL_VALUE."""
    values = values.astype("float64")
    if reasons is None:
        reasons = xr.where(np.isnan(values), FILL_VALUE, 0)
    reasons = reasons.astype("int8")
    ds = xr.Dataset({VALUE: values, REASON: reasons})
    ds[VALUE].attrs = {}
    return ds


def normalize_axes(ds: xr.Dataset) -> xr.Dataset:
    """Puts a grid in canonical axis order: (time, level, lat, lon), north
    first, west first, longitudes in -180 to 180."""
    if "lon" in ds.coords:
        lon = ds["lon"].values
        if np.nanmax(lon) > 180:
            ds = ds.assign_coords(lon=((lon + 180.0) % 360.0) - 180.0)
        ds = ds.sortby("lon")
    if "lat" in ds.coords:
        ds = ds.sortby("lat", ascending=False)
    order = [d for d in ("time", "level", "lat", "lon") if d in ds.dims]
    return ds.transpose(*order)


def combine(parts: list[xr.Dataset]) -> xr.Dataset:
    """Joins per-file pieces along time, sorted, keeping the first copy of a
    repeated time step."""
    if not parts:
        raise errors.no_data("No files arrived for this request.", "combine: no parts")
    if len(parts) == 1:
        ds = parts[0]
    else:
        ds = xr.concat(parts, dim="time", join="outer", coords="minimal", compat="override")
    ds = ds.sortby("time")
    _, first = np.unique(ds["time"].values, return_index=True)
    if len(first) != ds.sizes["time"]:
        ds = ds.isel(time=np.sort(first))
    return ds


def infer_step(times: np.ndarray) -> int:
    """Works out the time step from the spacing of the times."""
    if len(times) < 2:
        return common_pb2.TIME_STEP_MONTHLY
    gaps = np.diff(times.astype("datetime64[s]")).astype("int64") / 86400.0
    median = float(np.median(gaps))
    if median < 2:
        return common_pb2.TIME_STEP_DAILY
    if median < 10:
        return common_pb2.TIME_STEP_WEEKLY
    return common_pb2.TIME_STEP_MONTHLY


def snap_to_month_start(ds: xr.Dataset) -> xr.Dataset:
    """Monthly products stamp a month in different ways, like the 1st at
    00:00, the 1st at 00:30 or the 15th. The Series contract wants the start
    of the step, so monthly times become the 1st at 00:00."""
    times = pd.DatetimeIndex(ds["time"].values).to_period("M").to_timestamp()
    return ds.assign_coords(time=times.values.astype("datetime64[ns]"))


def regularize_monthly(ds: xr.Dataset) -> xr.Dataset:
    """Gives a monthly series every month from its first to its last, so a
    step's position says how far apart two steps are. Months a source skipped
    become missing with no known reason.

    Times are first moved to the 1st of their month. Without that, data
    stamped on another day, like the 15th, would match none of the months
    and every value would be lost."""
    if ds.sizes.get("time", 0) == 0:
        return ds
    ds = combine([snap_to_month_start(ds)])
    t = pd.DatetimeIndex(ds["time"].values)
    full = pd.date_range(t.min(), t.max(), freq="MS")
    if len(full) == len(t):
        return ds
    out = ds.reindex(time=full.values.astype("datetime64[ns]"))
    out[REASON] = out[REASON].fillna(data_pb2.MISSING_REASON_UNSPECIFIED).astype("int8")
    return out


def date_bounds(r: common_pb2.DateRange) -> tuple[np.datetime64, np.datetime64] | None:
    """The first and last instants a DateRange covers, both ends included."""
    if not r.HasField("start") or not r.HasField("end"):
        return None
    start = np.datetime64(f"{r.start.year:04d}-{r.start.month:02d}-{r.start.day:02d}", "ns")
    end_day = np.datetime64(f"{r.end.year:04d}-{r.end.month:02d}-{r.end.day:02d}", "ns")
    return start, end_day + np.timedelta64(1, "D") - np.timedelta64(1, "ns")


def trim(ds: xr.Dataset, dates: common_pb2.DateRange, step: int) -> xr.Dataset:
    """Keeps the steps that start inside the requested dates. A monthly step
    counts when its month overlaps the range, so a request from the 15th
    still gets that month."""
    bounds = date_bounds(dates)
    if bounds is None:
        return ds
    start, end = bounds
    if step == common_pb2.TIME_STEP_MONTHLY:
        start = np.datetime64(str(start)[:7], "ns")
    t = ds["time"].values
    return ds.isel(time=(t >= start) & (t <= end))


# How far, in degrees, a pixel center may sit outside the rectangle and still
# count as on its edge. Files store grid coordinates with rounding error:
# MERRA-2's 0 degrees east arrives from Harmony as -5.9e-13, and without this
# a rectangle from 0 to 20 east would lose its west column but keep its east
# one.
EDGE_TOLERANCE_DEG = 1e-6


def clip_bbox(ds: xr.Dataset, bbox: common_pb2.BBox | None) -> xr.Dataset:
    """Keeps pixels whose centers fall inside the rectangle, edges included.

    Harmony returns every pixel that touches the rectangle, so edge pixels can
    lie mostly outside it. A center on an edge counts as inside, on every
    edge alike, within EDGE_TOLERANCE_DEG. When a rectangle is smaller than one
    pixel, no center is inside it, and that axis is left as it came.
    """
    if bbox is None or not ("lat" in ds.dims and "lon" in ds.dims):
        return ds
    if bbox.west == bbox.east or bbox.south == bbox.north:
        return ds
    lat = ds["lat"].values
    lon = ds["lon"].values
    tol = EDGE_TOLERANCE_DEG
    lat_in = (lat >= bbox.south - tol) & (lat <= bbox.north + tol)
    lon_in = (lon >= bbox.west - tol) & (lon <= bbox.east + tol)
    if lat_in.any():
        ds = ds.isel(lat=lat_in)
    if lon_in.any():
        ds = ds.isel(lon=lon_in)
    return ds
