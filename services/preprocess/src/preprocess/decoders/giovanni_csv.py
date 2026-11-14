"""Giovanni time-series CSV, from the giovanni-area and giovanni-point adapters.

Both kinds start with metadata lines and then a table with a time column and
one value column:

- **Area average** (Harmony plus Giovanni): a few ``attr,value`` lines, a
  separator line (blank, or only ``,``), then ``time,mean_<variable ID>``.
  Missing days have an empty value, and some timestamps use a space instead
  of ``T``.
- **Point** (the time-series API): 13 ``key,value`` lines including
  ``param_name``, ``unit``, ``lat`` and ``lon``, then ``Timestamp,<name>``.

The table starts at the first line whose first cell is ``time`` or
``Timestamp``. These layouts come from NASA's tutorial notebooks and haven't
been checked against a file from our own adapters yet, which aren't built.
"""

from __future__ import annotations

import csv
import io
from pathlib import Path

import pandas as pd
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2

from .. import canonical, errors
from ..canonical import Decoded
from . import tabular
from .common import resolve_units

_TABLE_STARTS = {"time", "timestamp"}


def decode(paths: list[Path], raw: ingest_pb2.RawDataset, source_units: str) -> Decoded:
    notes: list[str] = []
    parts: list[xr.Dataset] = []
    declared = ""
    point = raw.point if raw.HasField("point") else None
    step = common_pb2.TIME_STEP_UNSPECIFIED

    for path in paths:
        meta, table = _split(path)
        declared = declared or meta.get("unit", "") or meta.get("units", "")
        if point is None and "lat" in meta and "lon" in meta:
            try:
                point = common_pb2.LatLon(lat=float(meta["lat"]), lon=float(meta["lon"]))
            except ValueError:
                pass
        fills = tuple(_floats(v for k, v in meta.items() if "fill" in k))
        times = tabular.parse_times(table.iloc[:, 0], path.name)
        values = tabular.parse_values(table.iloc[:, 1], fills)
        ds, step = tabular.to_dataset(times, values, path.name)
        parts.append(ds)

    ds = canonical.combine(parts)
    return Decoded(
        data=ds,
        units=resolve_units(declared, source_units, notes),
        step=step,
        point=point,
        notes=notes,
    )


def _split(path: Path) -> tuple[dict[str, str], pd.DataFrame]:
    try:
        lines = path.read_text(encoding="utf-8-sig").splitlines()
    except (OSError, UnicodeDecodeError) as err:
        raise errors.unreadable("Giovanni sent a file we couldn't read.", f"{path.name}: {err}") from err

    meta: dict[str, str] = {}
    for i, line in enumerate(lines):
        cells = next(csv.reader([line]), [])
        first = cells[0].strip().lower() if cells else ""
        if first in _TABLE_STARTS and len(cells) >= 2:
            table = pd.read_csv(
                io.StringIO("\n".join(lines[i:])), dtype=str, keep_default_na=False, usecols=[0, 1]
            )
            return meta, table
        if len(cells) >= 2 and first:
            meta[first] = cells[1].strip()
    raise errors.unreadable(
        "Giovanni sent a file we couldn't read.", f"{path.name}: no line starting with time or Timestamp"
    )


def _floats(values) -> list[float]:
    out = []
    for v in values:
        try:
            out.append(float(v))
        except ValueError:
            pass
    return out
