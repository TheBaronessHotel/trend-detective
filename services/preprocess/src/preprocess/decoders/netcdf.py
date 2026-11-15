"""NetCDF-4 grids from Harmony's HOSS subsetter, one file per granule.

What differs between products, and how it's handled:

- **Variable paths.** GPM IMERG keeps its data in a group, so the variable is
  ``Grid/precipitation``. MERRA-2 and GLDAS keep theirs at the root, like
  ``T2M``. AIRS uses ``ascending/Data_Fields/TotH2OVap_A``. Everything before
  the last ``/`` is the group.
- **Where the coordinates are.** Most products keep an axis's values next to
  the data. AIRS keeps them at the file's root (``YDim_ascending``,
  ``XDim_ascending``, ``StdPressureLev_ascending``) while the data sits in a
  group, so an axis with no values in the variable's own group takes them
  from the nearest group above it that has a variable of that name.
- **Axis names and order.** IMERG stores ``(time, lon, lat)``, the others
  ``(time, lat, lon)``. Axes are found by name or CF attributes, including
  AIRS's ``YDim``/``XDim``, and put in canonical order.
- **Pressure levels.** A profile like AIRS methane has a pressure axis
  (``*PressureLev*``, or a coordinate in hPa or Pa). It becomes ``level``, in
  hPa, and stays until a column_average step collapses it.
- **Helper variables.** The first variable in RawDataset.variables is the
  measurement. Any others, like AIRS surface pressure, are read alongside it
  as helpers a filter step can name.
- **Fill values and packing.** xarray applies ``_FillValue``,
  ``missing_value``, ``scale_factor`` and ``add_offset``, so fill values
  arrive as NaN.
- **Time stamps.** Monthly products stamp their months differently, like
  MERRA-2's 00:30. Monthly times are snapped to the 1st at 00:00. A granule
  with no time axis, like AIRS, takes its date from ingest's coverage.
  NOBM's monthly chlorophyll counts ``months since 1998-01-01``, which
  xarray won't decode, so such a file is opened without decoding times and
  its months (or years) are added to the reference date here.
"""

from __future__ import annotations

import re
import threading
from pathlib import Path

import netCDF4 as nc
import numpy as np
import pandas as pd
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2

from .. import canonical, errors
from ..canonical import Decoded
from .common import resolve_units

# The netCDF and HDF5 C libraries aren't safe to call from two threads at
# once, and the worker runs tasks in threads: two tasks reading files at the
# same time crashed the whole process with a segfault. Every file read goes
# through this lock; the rest of a task still runs in parallel.
_FILES = threading.Lock()

_LAT_NAMES = {"lat", "latitude", "nlat", "y"}
_LON_NAMES = {"lon", "longitude", "nlon", "x"}
_PRESSURE_UNITS = {"hpa": 1.0, "mb": 1.0, "mbar": 1.0, "millibar": 1.0, "pa": 0.01}

AXES = ("time", "level", "lat", "lon")


def decode(paths: list[Path], raw: ingest_pb2.RawDataset, source_units: str) -> Decoded:
    if not raw.variables:
        raise errors.invalid(
            "The data for this job couldn't be matched to a variable.", "RawDataset lists no variables"
        )
    variables = list(raw.variables)
    notes: list[str] = []
    parts: list[xr.Dataset] = []
    declared = ""

    for i, path in enumerate(paths):
        coverage = raw.files[i].coverage if i < len(raw.files) else None
        measurement, *helpers = _read(path, variables)
        declared = declared or str(measurement.attrs.get("units", ""))
        ds = canonical.make_dataset(_canonical_dims(measurement, path.name, coverage))
        for var, da in zip(variables[1:], helpers, strict=True):
            ds[canonical.helper_name(var)] = _to_hpa(_canonical_dims(da, path.name, coverage)).astype("float64")
        parts.append(ds)

    ds = canonical.combine(parts)
    step = canonical.infer_step(ds["time"].values)
    if step == common_pb2.TIME_STEP_MONTHLY:
        ds = canonical.snap_to_month_start(ds)
        ds = canonical.combine([ds])
    ds = canonical.normalize_axes(ds)
    return Decoded(
        data=ds,
        units=resolve_units(declared, source_units, notes),
        step=step,
        notes=notes,
    )


def _read(path: Path, variables: list[str]) -> list[xr.DataArray]:
    """Reads every variable from one file, opening each group once and the
    file's raw netCDF handle at most once, all under the file lock."""
    groups: dict[str, xr.Dataset] = {}
    handle: nc.Dataset | None = None
    out: list[xr.DataArray] = []
    try:
        with _FILES:
            try:
                for var in variables:
                    group, _, name = var.rpartition("/")
                    if group not in groups:
                        groups[group] = _open(path, group)
                    ds = groups[group]
                    if name not in ds.variables:
                        raise errors.invalid(
                            "The downloaded file doesn't have the variable this job asked for.",
                            f"{path.name}: no {name!r} in group {group or '/'}; has {sorted(map(str, ds.data_vars))}",
                        )
                    # load() reads the data before the file closes.
                    da = ds[name].load()
                    missing = [str(d) for d in da.dims if d not in da.coords]
                    if missing and group:
                        handle = handle or nc.Dataset(path)
                        da = _coords_from_parents(handle, group, da, missing)
                    out.append(da)
            finally:
                for ds in groups.values():
                    ds.close()
                if handle is not None:
                    handle.close()
    except errors.TaskFailure:
        raise
    except (OSError, ValueError, KeyError, RuntimeError) as err:
        raise errors.unreadable(
            "NASA sent a file we couldn't read.", f"{path.name}: {type(err).__name__}: {err}"
        ) from err
    return out


def _open(path: Path, group: str) -> xr.Dataset:
    """Opens one group. Times in months or years since a date, like NOBM's
    "months since 1998-01-01", have no fixed length, so xarray refuses them;
    such a file opens with its times as numbers, for _as_datetime64."""
    try:
        return xr.open_dataset(path, group=group or None, engine="netcdf4", mask_and_scale=True)
    except ValueError as err:
        if "unable to decode time units" not in str(err):
            raise
        return xr.open_dataset(path, group=group or None, engine="netcdf4", mask_and_scale=True, decode_times=False)


def _coords_from_parents(handle: nc.Dataset, group: str, da: xr.DataArray, dims: list[str]) -> xr.DataArray:
    """Gives each axis in dims the values of the nearest 1-D variable of the
    same name in the variable's group or a group above it, up to the root.

    It reads those few values straight through netCDF4: opening each parent
    group with xarray took most of a second per file on AIRS, inside the
    lock every task's file reads share."""
    chain = [handle]
    for part in group.split("/"):
        chain.append(chain[-1].groups[part])
    for g in reversed(chain):
        for d in list(dims):
            v = g.variables.get(d)
            if v is None or v.dimensions != (d,):
                continue
            values = np.ma.filled(np.ma.asarray(v[:], dtype="float64"), np.nan)
            attrs = {k: v.getncattr(k) for k in ("units", "standard_name", "long_name") if k in v.ncattrs()}
            da = da.assign_coords({d: xr.DataArray(values, dims=d, attrs=attrs)})
            dims.remove(d)
        if not dims:
            break
    return da


def _to_hpa(helper: xr.DataArray) -> xr.DataArray:
    """A helper whose units say it's a pressure, in hPa. Others pass through."""
    factor = _PRESSURE_UNITS.get(str(helper.attrs.get("units", "")).strip().lower())
    if factor is None or factor == 1.0:
        return helper
    return helper * factor


def _canonical_dims(
    da: xr.DataArray, file_name: str, coverage: common_pb2.DateRange | None
) -> xr.DataArray:
    renames: dict[str, str] = {}
    for dim in da.dims:
        kind = _axis_kind(da, str(dim))
        if kind and kind != dim:
            renames[str(dim)] = kind
    if len(set(renames.values())) != len(renames):
        raise errors.unreadable(
            "NASA sent a file we couldn't read.", f"{file_name}: two axes of the same kind in {da.dims}"
        )
    # An axis found by its name alone, like YDim, has no positions unless some
    # group holds its values. Without them xarray counts 0, 1, 2, and the map
    # would land at the wrong place without any error.
    for dim in da.dims:
        kind = renames.get(str(dim), str(dim))
        if kind in ("lat", "lon", "level") and dim not in da.coords:
            raise errors.unreadable(
                "NASA sent a file we couldn't read.",
                f"{file_name}: axis {dim!r} has no coordinate values in its group or any group above",
            )
    da = da.rename(renames)
    # A single pressure level is a plain map, like any other axis of size 1.
    if "level" in da.dims and da.sizes["level"] == 1:
        da = da.squeeze("level", drop=True)

    if "time" not in da.dims:
        # A granule with no time axis covers one step, which ingest knows.
        start = canonical.date_bounds(coverage) if coverage is not None else None
        if start is None:
            raise errors.unreadable(
                "NASA sent a file without dates.", f"{file_name}: no time axis and no coverage"
            )
        da = da.expand_dims(time=[start[0]])

    extra = [d for d in da.dims if d not in AXES]
    for d in extra:
        if da.sizes[d] != 1:
            raise errors.unsupported(
                "This product has more layers than the app can show yet.",
                f"{file_name}: extra dimension {d!r} of size {da.sizes[d]}",
            )
    da = da.squeeze(extra, drop=True)
    if "lat" not in da.dims or "lon" not in da.dims:
        raise errors.unreadable(
            "NASA sent a file we couldn't read.", f"{file_name}: no lat/lon axes in {da.dims}"
        )

    coords = {
        "time": _as_datetime64(da["time"]),
        "lat": da["lat"].values.astype("float64"),
        "lon": da["lon"].values.astype("float64"),
    }
    if "level" in da.dims:
        coords["level"] = _hpa(da["level"])
    da = da.assign_coords(coords)
    return da.drop_vars([c for c in da.coords if c not in AXES])


def _axis_kind(da: xr.DataArray, dim: str) -> str | None:
    low = dim.lower()
    attrs = da[dim].attrs if dim in da.coords else {}
    standard = str(attrs.get("standard_name", "")).lower()
    units = str(attrs.get("units", "")).lower()
    if low == "time" or standard == "time":
        return "time"
    if low in _LAT_NAMES or low.startswith("ydim") or standard == "latitude" or units in ("degrees_north", "degree_north"):
        return "lat"
    if low in _LON_NAMES or low.startswith("xdim") or standard == "longitude" or units in ("degrees_east", "degree_east"):
        return "lon"
    if "pressurelev" in low or standard == "air_pressure" or (dim in da.coords and units in _PRESSURE_UNITS):
        return "level"
    return None


def _hpa(level: xr.DataArray) -> np.ndarray:
    """Pressure levels in hPa. Values without units count as hPa already."""
    factor = _PRESSURE_UNITS.get(str(level.attrs.get("units", "hPa")).lower(), 1.0)
    return level.values.astype("float64") * factor


# "months since 1998-01-01 00:00:00", the units xarray won't decode.
_CALENDAR_UNITS = re.compile(r"^\s*(months?|years?)\s+since\s+(\d{1,4})-(\d{1,2})", re.IGNORECASE)


def _as_datetime64(time: xr.DataArray) -> np.ndarray:
    values = time.values
    if np.issubdtype(values.dtype, np.datetime64):
        return values.astype("datetime64[ns]")
    m = _CALENDAR_UNITS.match(str(time.attrs.get("units", "")))
    if m and np.issubdtype(values.dtype, np.number):
        # Whole months (or years) after the reference month; a fraction, like
        # a mid-month stamp of 181.5, stays in its month.
        per = 12 if m.group(1).lower().startswith("year") else 1
        start = int(m.group(2)) * 12 + int(m.group(3)) - 1
        months = [start + int(np.floor(float(v) * per + 1e-9)) for v in values]
        return np.array([f"{n // 12:04d}-{n % 12 + 1:02d}-01" for n in months], dtype="datetime64[ns]")
    # cftime objects, from calendars xarray can't map to numpy.
    return pd.to_datetime([str(v) for v in values]).values.astype("datetime64[ns]")
