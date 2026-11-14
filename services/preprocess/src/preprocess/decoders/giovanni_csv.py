"""Giovanni time-series CSVs, from the giovanni-area and giovanni-point adapters.

Both kinds start with metadata lines and then a table with a time column and
one value column:

- **Area average**, ``FORMAT_GIOVANNI_CSV``: a few ``key:,value`` lines, a
  blank line, then ``time,mean_<variable ID>``. Missing days have an empty
  value, and some timestamps use a space instead of ``T``.
- **Point**, ``FORMAT_GIOVANNI_POINT_CSV``: ``key,value`` lines including
  ``unit``, ``undef``, ``lat`` and ``lon``, a blank line, then
  ``Timestamp (UTC),Data``. Missing steps hold the ``undef`` value, such as
  ``-9999`` or ``-1.26765e+30``.

The table starts at the first line whose first cell starts with ``time`` or
``timestamp``. Header keys are matched without case and without a trailing
colon. The layouts are the ones services/ingest/README.md describes.
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

_TABLE_STARTS = ("time", "timestamp")


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
        fills = tuple(_floats(v for k, v in meta.items() if k == "undef" or "fill" in k))
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
        first = cells[0].strip().lower().rstrip(":").strip() if cells else ""
        if first.startswith(_TABLE_STARTS) and len(cells) >= 2:
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
