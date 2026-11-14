"""Builds the Series and Grid documents from the filtered data.

A grid becomes a region series by a cos(latitude)-weighted mean of its valid
pixels. Values and anomalies are averaged separately, so the region anomaly is
the mean of the per-pixel anomalies, as the contract asks. PRE-3 adds the N×N
sub-regions.
"""

from __future__ import annotations

import csv
import io
from dataclasses import dataclass

import numpy as np
import xarray as xr
from google.protobuf import timestamp_pb2

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from .canonical import Decoded
from .filters import anomaly as anomaly_mod

DEFAULT_MIN_VALID_FRACTION = 0.5


@dataclass
class GridOutput:
    """A Grid document, still missing its BlobRefs, and the bytes for them."""

    grid: data_pb2.Grid
    values: bytes
    anomalies: bytes | None = None


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
    times = dec.data["time"].values
    anom_layer = dec.anomalies
    if dec.gridded:
        values, coverage, reasons = _region_values(dec, aggregation)
        anomalies = None
        if anom_layer is not None:
            anomalies, _ = _weighted_mean(anom_layer.values, dec.data["lat"].values)
    else:
        values = dec.values.values.astype("float64")
        coverage = np.where(np.isnan(values), 0.0, 1.0)
        reasons = dec.reasons.values
        anomalies = anom_layer.values.astype("float64") if anom_layer is not None else None

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
    series.times.extend(timestamps(times))
    for i in np.flatnonzero(np.isnan(values)):
        series.missing.add(index=int(i), reason=int(reasons[i]))

    if dec.anomaly is not None and anomalies is not None:
        op = dec.anomaly
        base = (op.baseline.start_year, op.baseline.end_year)
        n = anomaly_mod.normals(values[:, None], times, op, base)
        usable = n.usable[:, 0][_month_index(times)]
        # A step that doesn't count as a value doesn't count as an anomaly, and
        # a month without a usable region normal has none.
        anomalies = np.where(np.isnan(values) | ~usable, np.nan, anomalies)
        series.anomalies.extend(anomalies.tolist())
        series.anomaly_kind = op.kind
        series.baseline.CopyFrom(op.baseline)
        series.normals.extend(anomaly_mod.monthly_normals(n))

    if dec.gap_fill_max > 0:
        series.display_values.extend(gap_fill(values, dec.gap_fill_max).tolist())
    return series


def _month_index(times: np.ndarray) -> np.ndarray:
    return times.astype("datetime64[M]").astype(int) % 12


def _weighted_mean(v: np.ndarray, lats: np.ndarray) -> tuple[np.ndarray, np.ndarray]:
    """The cos(lat)-weighted mean of each time step's valid pixels, and which
    pixels were valid."""
    valid = ~np.isnan(v)
    weights = np.cos(np.deg2rad(lats))[None, :, None] * np.ones_like(v)
    w_valid = np.where(valid, weights, 0.0).reshape(v.shape[0], -1)
    with np.errstate(invalid="ignore", divide="ignore"):
        mean = (np.where(valid, v, 0.0).reshape(v.shape[0], -1) * w_valid).sum(axis=1) / w_valid.sum(axis=1)
    return mean, valid


def _region_values(dec: Decoded, aggregation: preprocess_pb2.Aggregation) -> tuple[np.ndarray, np.ndarray, np.ndarray]:
    v = dec.values.values  # (time, lat, lon)
    mean, valid = _weighted_mean(v, dec.data["lat"].values)
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


def gap_fill(values: np.ndarray, max_gap: int) -> np.ndarray:
    """Fills runs of up to max_gap missing steps by a straight line between
    their neighbors, for drawing only. Gaps at either end stay empty, because
    there is nothing on one side to draw from."""
    out = values.astype("float64").copy()
    known = np.flatnonzero(~np.isnan(values))
    for a, b in zip(known[:-1], known[1:], strict=False):
        gap = b - a - 1
        if 0 < gap <= max_gap:
            out[a + 1 : b] = np.linspace(values[a], values[b], gap + 2)[1:-1]
    return out


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
    da = _fit(dec.values, aggregation.max_grid)
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
    out = GridOutput(grid=g, values=_f32(da))
    if dec.anomalies is not None:
        out.anomalies = _f32(_fit(dec.anomalies, aggregation.max_grid))
    return out


def _fit(da: xr.DataArray, max_grid: int) -> xr.DataArray:
    side = max(da.sizes["lat"], da.sizes["lon"])
    if max_grid > 0 and side > max_grid:
        factor = int(np.ceil(side / max_grid))
        # mean() skips NaN for floats, so a block's value is the mean of its
        # valid pixels, and padded edge blocks average what they have.
        da = da.coarsen(lat=factor, lon=factor, boundary="pad").mean()
    return da


def _f32(da: xr.DataArray) -> bytes:
    return np.ascontiguousarray(da.transpose("time", "lat", "lon").values, dtype="<f4").tobytes()


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


_KIND_COLUMN = {
    data_pb2.ANOMALY_KIND_ABSOLUTE: "anomaly",
    data_pb2.ANOMALY_KIND_PERCENT: "anomaly (%)",
    data_pb2.ANOMALY_KIND_STANDARDIZED: "anomaly (standard deviations)",
}


def series_csv(series: data_pb2.Series) -> bytes:
    """The series as CSV for download. Missing values are empty cells. The
    gap-filled display line isn't included, because it's only for drawing."""
    reasons = {m.index: data_pb2.MissingReason.Name(m.reason) for m in series.missing}
    unit = f" ({series.units})" if series.units else ""
    has_anomalies = len(series.anomalies) == len(series.times) and len(series.times) > 0
    header = ["time", f"value{unit}"]
    if has_anomalies:
        header.append(_KIND_COLUMN.get(series.anomaly_kind, "anomaly"))
    header += ["coverage", "missing_reason"]

    buf = io.StringIO()
    w = csv.writer(buf, lineterminator="\n")
    w.writerow(header)
    for i, ts in enumerate(series.times):
        row = [ts.ToDatetime().strftime("%Y-%m-%dT%H:%M:%SZ"), _cell(series.values[i])]
        if has_anomalies:
            row.append(_cell(series.anomalies[i]))
        row += [f"{series.coverage[i]:.4g}", reasons.get(i, "").removeprefix("MISSING_REASON_").lower()]
        w.writerow(row)
    return buf.getvalue().encode("utf-8")


def _cell(x: float) -> str:
    return "" if np.isnan(x) else repr(x)


def has_values(dec: Decoded) -> bool:
    return bool(np.any(~np.isnan(dec.values.values)))
