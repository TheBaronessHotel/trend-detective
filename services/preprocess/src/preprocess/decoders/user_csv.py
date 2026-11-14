"""A CSV the user uploaded: one column of dates and one column of values.

Exports from Giovanni, Hydrology Time Series and AppEEARS all fit this after
a little cleanup. The decoder takes the first column whose cells all read as
dates, and the first other column that is mostly numbers. Dates can be
``YYYY-MM``, ``YYYY-MM-DD`` or a full timestamp. Empty cells are missing.
"""

from __future__ import annotations

import io
from pathlib import Path

import pandas as pd

from trenddetective.ingest.v1 import ingest_pb2

from .. import errors
from ..canonical import Decoded
from . import tabular
from .common import resolve_units


def decode(paths: list[Path], raw: ingest_pb2.RawDataset, source_units: str) -> Decoded:
    if len(paths) != 1:
        raise errors.invalid(
            "Upload one CSV file at a time.", f"csv-upload dataset has {len(paths)} files"
        )
    path = paths[0]
    try:
        text = path.read_text(encoding="utf-8-sig")
        df = pd.read_csv(io.StringIO(text), sep=None, engine="python", dtype=str, keep_default_na=False)
    except (OSError, UnicodeDecodeError, pd.errors.ParserError, pd.errors.EmptyDataError) as err:
        raise errors.invalid("The uploaded file isn't a CSV we can read.", f"{path.name}: {err}") from err

    date_col = _date_column(df)
    value_col = _value_column(df, date_col)
    if date_col is None or value_col is None:
        raise errors.invalid(
            "The CSV needs one column of dates and one column of numbers.",
            f"{path.name}: columns {list(df.columns)}",
        )
    times = tabular.parse_times(df[date_col], path.name)
    values = tabular.parse_values(df[value_col])
    ds, step = tabular.to_dataset(times, values, path.name)
    notes: list[str] = []
    return Decoded(
        data=ds,
        units=resolve_units("", source_units, notes),
        step=step,
        point=raw.point if raw.HasField("point") else None,
        notes=notes,
    )


def _date_column(df: pd.DataFrame) -> str | None:
    for col in df.columns:
        cells = df[col].str.strip()
        cells = cells[cells != ""]
        if cells.empty:
            continue
        parsed = pd.to_datetime(cells.str.replace(" ", "T", n=1, regex=False), errors="coerce", format="mixed")
        if parsed.notna().all() and not pd.to_numeric(cells, errors="coerce").notna().all():
            return col
    return None


def _value_column(df: pd.DataFrame, date_col: str | None) -> str | None:
    for col in df.columns:
        if col == date_col:
            continue
        cells = df[col].str.strip()
        cells = cells[cells != ""]
        if not cells.empty and pd.to_numeric(cells, errors="coerce").notna().mean() >= 0.9:
            return col
    return None
