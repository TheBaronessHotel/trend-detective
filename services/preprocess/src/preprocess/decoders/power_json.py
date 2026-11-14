"""NASA POWER monthly point JSON, from the power and bundle adapters.

The file is GeoJSON. ``properties.parameter.<VAR>`` maps ``YYYYMM`` keys to
values. Two quirks:

- Every complete year has a 13th "month", ``YYYY13``, holding the annual
  mean. It isn't a time step, so it's dropped.
- Missing values are ``header.fill_value``, -999.

The monthly endpoint only takes whole years, so a file usually covers more
than the request. Trimming to the requested dates happens after decoding.
"""

from __future__ import annotations

import json
from pathlib import Path

import numpy as np
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2

from .. import canonical, errors
from ..canonical import Decoded
from .common import resolve_units, single_variable

DEFAULT_FILL = -999.0


def decode(paths: list[Path], raw: ingest_pb2.RawDataset, source_units: str) -> Decoded:
    var = single_variable(raw)
    notes: list[str] = []
    parts: list[xr.Dataset] = []
    declared = ""
    point = raw.point if raw.HasField("point") else None

    for path in paths:
        try:
            doc = json.loads(path.read_text(encoding="utf-8"))
            series = doc["properties"]["parameter"][var]
        except (OSError, ValueError, KeyError, TypeError) as err:
            raise errors.unreadable(
                "NASA POWER sent a file we couldn't read.",
                f"{path.name}: no properties.parameter.{var}: {err}",
            ) from err
        header = doc.get("header") or {}
        fill = float(header.get("fill_value", DEFAULT_FILL))
        declared = declared or ((doc.get("parameters") or {}).get(var) or {}).get("units", "")
        if point is None:
            coords = (doc.get("geometry") or {}).get("coordinates") or []
            if len(coords) >= 2:
                point = common_pb2.LatLon(lat=float(coords[1]), lon=float(coords[0]))
        parts.append(_to_dataset(series, fill, path.name))

    ds = canonical.combine(parts)
    return Decoded(
        data=ds,
        units=resolve_units(declared, source_units, notes),
        step=common_pb2.TIME_STEP_MONTHLY,
        point=point,
        notes=notes,
    )


def _to_dataset(series: dict, fill: float, name: str) -> xr.Dataset:
    times: list[np.datetime64] = []
    values: list[float] = []
    for key, value in series.items():
        if len(key) != 6 or not key.isdigit():
            raise errors.unreadable(
                "NASA POWER sent a file we couldn't read.", f"{name}: unexpected key {key!r}"
            )
        month = int(key[4:])
        if month == 13:
            continue
        if not 1 <= month <= 12:
            raise errors.unreadable(
                "NASA POWER sent a file we couldn't read.", f"{name}: unexpected month in {key!r}"
            )
        times.append(np.datetime64(f"{key[:4]}-{key[4:]}-01", "ns"))
        v = float("nan") if value is None else float(value)
        values.append(float("nan") if v == fill else v)
    if not times:
        raise errors.no_data("NASA POWER has no monthly values for this request.", f"{name}: empty")
    arr = xr.DataArray(np.array(values, dtype="float64"), dims=("time",), coords={"time": times})
    return canonical.make_dataset(arr)
