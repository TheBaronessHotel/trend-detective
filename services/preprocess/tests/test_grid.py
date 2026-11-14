"""The Grid's square blocks: block size, weights, coverage and edges (PRE-3)."""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest
import xarray as xr
from google.protobuf import json_format

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2, preprocess_pb2

from conftest import raw_dataset, write_imerg_like
from preprocess import canonical, output, pipeline
from preprocess.canonical import Decoded
from preprocess.storage import MemoryStorage

Agg = preprocess_pb2.Aggregation


@pytest.mark.parametrize(
    ("rows", "cols", "agg", "k"),
    [
        (32, 32, Agg(block_pixels=4, subregion_n=8), 4),  # block_pixels wins
        (32, 32, Agg(subregion_n=8), 4),  # else from subregion_n
        (33, 20, Agg(subregion_n=8), 5),  # ceil of the longer side
        (32, 32, Agg(), 1),  # else native
        (12, 12, Agg(block_pixels=8), 4),  # at least 3 cells on the shorter side
        (8, 32, Agg(subregion_n=8), 2),  # the short side keeps 4 cells of 2
        (8, 8, Agg(block_pixels=1, max_grid=3), 3),  # max_grid wins over everything
        (32, 32, Agg(block_pixels=2, max_grid=4), 8),
        (2, 2, Agg(block_pixels=4), 1),  # too small to block at all
    ],
)
def test_block_size(rows: int, cols: int, agg: preprocess_pb2.Aggregation, k: int):
    assert output.block_size(rows, cols, agg) == k


def decoded(v: np.ndarray, lats: list[float], lons: list[float], anomalies: np.ndarray | None = None) -> Decoded:
    times = np.array([f"2020-{m:02d}-01" for m in range(1, v.shape[0] + 1)], dtype="datetime64[ns]")
    da = xr.DataArray(v, dims=("time", "lat", "lon"), coords={"time": times, "lat": lats, "lon": lons})
    ds = canonical.make_dataset(da)
    if anomalies is not None:
        ds[canonical.ANOMALY] = xr.DataArray(anomalies, dims=("time", "lat", "lon"), coords=da.coords)
    return Decoded(data=ds, units="K", step=common_pb2.TIME_STEP_MONTHLY)


def grid_values(out: output.GridOutput, blob: bytes | None = None) -> np.ndarray:
    g = out.grid
    return np.frombuffer(blob if blob is not None else out.values, "<f4").reshape(len(g.times), g.rows, g.cols)


def test_square_blocks_with_partial_edges_and_weights():
    # 7 x 7 pixels, 10° apart, in blocks of 2: the last row and column of cells
    # hold the leftover pixel.
    lats = [60.0, 50.0, 40.0, 30.0, 20.0, 10.0, 0.0]
    lons = [0.0, 1.0, 2.0, 3.0, 4.0, 5.0, 6.0]
    v = np.broadcast_to(np.array(lats)[:, None], (7, 7))[None].astype("float64").copy()
    out = output.grid(decoded(v, lats, lons), "t", Agg(block_pixels=2))
    g = out.grid

    assert (g.rows, g.cols, g.block_pixels) == (4, 4, 2)
    assert list(g.cell_pixels) == [4, 4, 4, 2] * 3 + [2, 2, 2, 1]
    assert list(g.lat_edges) == [65.0, 45.0, 25.0, 5.0, -5.0]
    assert list(g.lon_edges) == [-0.5, 1.5, 3.5, 5.5, 6.5]
    assert list(g.lats) == [55.0, 35.0, 15.0, 0.0]
    assert list(g.lons) == [0.5, 2.5, 4.5, 6.0]
    cells = grid_values(out)[0]
    # The top cell averages 60° and 50°, weighted by cos(lat), so it leans to 50.
    w = np.cos(np.deg2rad([60.0, 50.0]))
    assert cells[0, 0] == pytest.approx((60 * w[0] + 50 * w[1]) / w.sum(), rel=1e-6)
    assert cells[3, 3] == pytest.approx(0.0, abs=1e-6)


def test_cells_use_the_coverage_rule():
    # 6 x 6 pixels in blocks of 2. The top-right cell is ocean, never valid. In
    # the second month, only one pixel of the top-left cell is valid.
    v = np.arange(72, dtype="float64").reshape(2, 6, 6)
    v[:, 0:2, 4:6] = np.nan
    v[1, 0, 1] = v[1, 1, 0] = v[1, 1, 1] = np.nan
    lats = [5.0, 4.0, 3.0, 2.0, 1.0, 0.0]
    out = output.grid(
        decoded(v, lats, [0.0, 1.0, 2.0, 3.0, 4.0, 5.0]), "t", Agg(block_pixels=2, min_valid_fraction=0.5)
    )
    cells = grid_values(out)

    assert list(out.grid.cell_pixels) == [4, 4, 0, 4, 4, 4, 4, 4, 4]
    assert np.isnan(cells[:, 0, 2]).all()  # the ocean cell
    assert not np.isnan(cells[0, 0, 0])
    assert np.isnan(cells[1, 0, 0])  # 1 of 4 valid, under half
    assert not np.isnan(cells[1, 0, 1])


def test_anomalies_follow_the_values_mask():
    v = np.ones((2, 6, 6))
    v[1, 0, 1] = v[1, 1, 0] = v[1, 1, 1] = np.nan  # second month: 1 of 4 valid in the top-left cell
    a = np.full((2, 6, 6), 0.5)
    lats = [5.0, 4.0, 3.0, 2.0, 1.0, 0.0]
    out = output.grid(decoded(v, lats, lats[::-1], anomalies=a), "t", Agg(block_pixels=2))
    anomalies = grid_values(out, out.anomalies)

    assert anomalies[0, 0, 0] == pytest.approx(0.5)
    assert np.isnan(anomalies[1, 0, 0])
    assert anomalies[1, 1, 1] == pytest.approx(0.5)


def test_block_size_never_changes_the_region_series(tmp_path: Path):
    store = MemoryStorage()
    path = tmp_path / "a.nc4"
    write_imerg_like(path, ["2020-01-01", "2020-02-01"], np.arange(12) + 0.5, np.arange(12) + 0.5)
    store.put("raw", "h/a.nc4", path.read_bytes(), "application/x-netcdf")

    def run(block: int) -> tuple[data_pb2.Series, data_pb2.Grid]:
        task = preprocess_pb2.PreprocessTask()
        task.raw.append(raw_dataset(ingest_pb2.FORMAT_NETCDF4, ["Grid/precipitation"], ["h/a.nc4"]))
        task.aggregation.block_pixels = block
        result, _ = pipeline.run(task, store)
        series = json_format.Parse(store.get(result.series.bucket, result.series.key), data_pb2.Series())
        grid = json_format.Parse(store.get(result.grid.bucket, result.grid.key), data_pb2.Grid())
        return series, grid

    native_series, native_grid = run(1)
    blocked_series, blocked_grid = run(4)
    assert (native_grid.rows, blocked_grid.rows) == (12, 3)
    assert list(native_series.values) == list(blocked_series.values)
