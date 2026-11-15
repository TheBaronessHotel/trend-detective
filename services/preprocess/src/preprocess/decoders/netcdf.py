"""NetCDF-4 grids from Harmony's HOSS subsetter, one file per granule.

What differs between products, and how it's handled:

- **Variable paths.** GPM IMERG keeps its data in a group, so the variable is
  ``Grid/precipitation``. MERRA-2 and GLDAS keep theirs at the root, like
  ``T2M``. Everything before the last ``/`` is the group.
- **Axis names and order.** IMERG stores ``(time, lon, lat)``, the others
  ``(time, lat, lon)``. Axes are found by name or CF attributes and put in
  canonical order.
- **Fill values and packing.** xarray applies ``_FillValue``,
  ``missing_value``, ``scale_factor`` and ``add_offset``, so fill values
  arrive as NaN.
- **Time stamps.** Monthly products stamp their months differently, like
  MERRA-2's 00:30. Monthly times are snapped to the 1st at 00:00.
"""

from __future__ import annotations

import threading
from pathlib import Path

import numpy as np
import pandas as pd
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2

from .. import canonical, errors
from ..canonical import Decoded
from .common import resolve_units, single_variable

# The netCDF and HDF5 C libraries aren't safe to call from two threads at
# once, and the worker runs tasks in threads: two tasks reading files at the
# same time crashed the whole process with a segfault. Every file read goes
# through this lock; the rest of a task still runs in parallel.
_FILES = threading.Lock()

_LAT_NAMES = {"lat", "latitude", "nlat", "y"}
_LON_NAMES = {"lon", "longitude", "nlon", "x"}


def decode(paths: list[Path], raw: ingest_pb2.RawDataset, source_units: str) -> Decoded:
    var = single_variable(raw)
    group, _, name = var.rpartition("/")
    notes: list[str] = []
    parts: list[xr.Dataset] = []
    declared = ""

    for i, path in enumerate(paths):
        coverage = raw.files[i].coverage if i < len(raw.files) else None
        da = _read(path, group or None, name)
        declared = declared or str(da.attrs.get("units", ""))
        parts.append(canonical.make_dataset(_canonical_dims(da, path.name, coverage)))

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


def _read(path: Path, group: str | None, name: str) -> xr.DataArray:
    try:
        # load() reads the data before the file closes, all under the lock.
        with _FILES, xr.open_dataset(path, group=group, engine="netcdf4", mask_and_scale=True) as ds:
            if name not in ds.variables:
                raise errors.invalid(
                    "The downloaded file doesn't have the variable this job asked for.",
                    f"{path.name}: no {name!r} in group {group or '/'}; has {sorted(map(str, ds.data_vars))}",
                )
            return ds[name].load()
    except errors.TaskFailure:
        raise
    except (OSError, ValueError, KeyError) as err:
        raise errors.unreadable(
            "NASA sent a file we couldn't read.", f"{path.name}: {type(err).__name__}: {err}"
        ) from err


def _canonical_dims(
    da: xr.DataArray, file_name: str, coverage: common_pb2.DateRange | None
) -> xr.DataArray:
    renames: dict[str, str] = {}
    for dim in da.dims:
        kind = _axis_kind(da, str(dim))
        if kind and kind != dim:
            renames[str(dim)] = kind
    da = da.rename(renames)

    if "time" not in da.dims:
        # A granule with no time axis covers one step, which ingest knows.
        start = canonical.date_bounds(coverage) if coverage is not None else None
        if start is None:
            raise errors.unreadable(
                "NASA sent a file without dates.", f"{file_name}: no time axis and no coverage"
            )
        da = da.expand_dims(time=[start[0]])

    extra = [d for d in da.dims if d not in ("time", "lat", "lon")]
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

    da = da.assign_coords(
        time=_as_datetime64(da["time"]),
        lat=da["lat"].values.astype("float64"),
        lon=da["lon"].values.astype("float64"),
    )
    return da.drop_vars([c for c in da.coords if c not in ("time", "lat", "lon")])


def _axis_kind(da: xr.DataArray, dim: str) -> str | None:
    low = dim.lower()
    attrs = da[dim].attrs if dim in da.coords else {}
    standard = str(attrs.get("standard_name", "")).lower()
    units = str(attrs.get("units", "")).lower()
    if low == "time" or standard == "time":
        return "time"
    if low in _LAT_NAMES or standard == "latitude" or units in ("degrees_north", "degree_north"):
        return "lat"
    if low in _LON_NAMES or standard == "longitude" or units in ("degrees_east", "degree_east"):
        return "lon"
    return None


def _as_datetime64(time: xr.DataArray) -> np.ndarray:
    values = time.values
    if np.issubdtype(values.dtype, np.datetime64):
        return values.astype("datetime64[ns]")
    # cftime objects, from calendars xarray can't map to numpy.
    return pd.to_datetime([str(v) for v in values]).values.astype("datetime64[ns]")
