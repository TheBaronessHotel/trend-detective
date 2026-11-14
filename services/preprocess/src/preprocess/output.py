"""Builds the Series and Grid documents from decoded data.

This is the passthrough version for the walking skeleton. Values go out as
decoded, with no filter chain (PRE-2), and a grid becomes a region series by a
plain cos(latitude)-weighted mean of the valid pixels (PRE-3 replaces it with
anomalies first, then averaging, plus the N×N sub-regions).
"""

from __future__ import annotations

import csv
import io
from dataclasses import dataclass

import numpy as np
from google.protobuf import timestamp_pb2

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from .canonical import Decoded

DEFAULT_MIN_VALID_FRACTION = 0.5


@dataclass
class GridOutput:
    """A Grid document, still missing its values BlobRef, and the bytes for it."""

    grid: data_pb2.Grid
    values: bytes


def timestamps(times: np.ndarray) -> list[timestamp_pb2.Timestamp]:
    out = []
    for t in times.astype("datetime64[ns]").astype("int64"):
        ts = timestamp_pb2.Timestamp()
        ts.FromNanoseconds(int(t))
        out.append(ts)
    return out


def region_series(
    dec: Decoded,
    raw: ingest_pb2.RawDataset,
    variable_id: str,
    aggregation: preprocess_pb2.Aggregation,
) -> data_pb2.Series:
    if dec.gridded:
        values, coverage, reasons = _weighted_mean(dec, aggregation)
    else:
        values = dec.values.values.astype("float64")
        coverage = np.where(np.isnan(values), 0.0, 1.0)
        reasons = dec.reasons.values

    series = data_pb2.Series(
        variable_id=variable_id,
        units=dec.units,
        source=raw.source,
        step=dec.step,
        values=values.tolist(),
        coverage=coverage.tolist(),
    )
    if raw.HasField("bbox"):
        series.bbox.CopyFrom(raw.bbox)
    series.times.extend(timestamps(dec.data["time"].values))
    for i in np.flatnonzero(np.isnan(values)):
        series.missing.add(index=int(i), reason=int(reasons[i]))
    return series


def _weighted_mean(
    dec: Decoded, aggregation: preprocess_pb2.Aggregation
) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    v = dec.values.values  # (time, lat, lon)
    valid = ~np.isnan(v)
    weights = np.cos(np.deg2rad(dec.data["lat"].values))[None, :, None] * np.ones_like(v)
    w_valid = np.where(valid, weights, 0.0)
    with np.errstate(invalid="ignore", divide="ignore"):
        mean = np.where(valid, v, 0.0).reshape(v.shape[0], -1)
        mean = (mean * w_valid.reshape(v.shape[0], -1)).sum(axis=1) / w_valid.reshape(v.shape[0], -1).sum(axis=1)
    # Coverage counts only pixels that have data at least once in the record.
    # Pixels that never do, like ocean in a land-only product such as GLDAS or
    # latitudes a satellite never sees, act as a mask. Counting them would
    # push every month of a coastal rectangle below min_valid_fraction.
    pixels = int(valid.any(axis=0).sum())
    coverage = valid.reshape(v.shape[0], -1).sum(axis=1) / max(pixels, 1)

    min_fraction = aggregation.min_valid_fraction or DEFAULT_MIN_VALID_FRACTION
    reasons = np.zeros(v.shape[0], dtype="int8")
    for t in range(v.shape[0]):
        if coverage[t] == 0:
            reasons[t] = _dominant(dec.reasons.values[t][~valid[t]])
            mean[t] = np.nan
        elif coverage[t] < min_fraction:
            reasons[t] = data_pb2.MISSING_REASON_LOW_COVERAGE
            mean[t] = np.nan
    return mean, coverage, reasons


def _dominant(reasons: np.ndarray) -> int:
    if reasons.size == 0:
        return data_pb2.MISSING_REASON_UNSPECIFIED
    codes, counts = np.unique(reasons, return_counts=True)
    return int(codes[np.argmax(counts)])


def grid(
    dec: Decoded,
    variable_id: str,
    aggregation: preprocess_pb2.Aggregation,
    bbox: common_pb2.BBox | None = None,
) -> GridOutput:
    """The pixels at native resolution, block-averaged down when a side is
    longer than max_grid.

    bbox is the requested rectangle, the same one Series.bbox carries. Without
    one, the grid's outer pixel edges are used.
    """
    da = dec.values
    max_grid = aggregation.max_grid
    side = max(da.sizes["lat"], da.sizes["lon"])
    if max_grid > 0 and side > max_grid:
        factor = int(np.ceil(side / max_grid))
        # mean() skips NaN for floats, so a block's value is the mean of its
        # valid pixels, and padded edge blocks average what they have.
        da = da.coarsen(lat=factor, lon=factor, boundary="pad").mean()

    rows, cols = da.sizes["lat"], da.sizes["lon"]
    g = data_pb2.Grid(
        variable_id=variable_id,
        units=dec.units,
        rows=rows,
        cols=cols,
        lats=da["lat"].values.astype("float64").tolist(),
        lons=da["lon"].values.astype("float64").tolist(),
        step=dec.step,
    )
    # The extent comes from the native pixels: block-averaged ones are unevenly
    # spaced when the last block is padded.
    g.bbox.CopyFrom(bbox if bbox is not None else _pixel_extent(dec.data["lat"].values, dec.data["lon"].values))
    g.times.extend(timestamps(da["time"].values))
    data = np.ascontiguousarray(da.transpose("time", "lat", "lon").values, dtype="<f4").tobytes()
    return GridOutput(grid=g, values=data)


def _pixel_extent(lats: np.ndarray, lons: np.ndarray) -> common_pb2.BBox:
    """The outer edges of the pixels, from their centers. A single pixel has no
    spacing to go by, so its extent is its center."""
    half_lat = abs(float(np.diff(lats).mean())) / 2 if len(lats) > 1 else 0.0
    half_lon = abs(float(np.diff(lons).mean())) / 2 if len(lons) > 1 else 0.0
    return common_pb2.BBox(
        west=float(lons.min()) - half_lon,
        south=float(lats.min()) - half_lat,
        east=float(lons.max()) + half_lon,
        north=float(lats.max()) + half_lat,
    )


def series_csv(series: data_pb2.Series) -> bytes:
    """The series as CSV for download. Missing values are empty cells."""
    reasons = {m.index: data_pb2.MissingReason.Name(m.reason) for m in series.missing}
    unit = f" ({series.units})" if series.units else ""
    buf = io.StringIO()
    w = csv.writer(buf, lineterminator="\n")
    w.writerow(["time", f"value{unit}", "coverage", "missing_reason"])
    for i, ts in enumerate(series.times):
        value = series.values[i]
        w.writerow(
            [
                ts.ToDatetime().strftime("%Y-%m-%dT%H:%M:%SZ"),
                "" if np.isnan(value) else repr(value),
                f"{series.coverage[i]:.4g}",
                reasons.get(i, "").removeprefix("MISSING_REASON_").lower(),
            ]
        )
    return buf.getvalue().encode("utf-8")


def has_values(dec: Decoded) -> bool:
    return bool(np.any(~np.isnan(dec.values.values)))
