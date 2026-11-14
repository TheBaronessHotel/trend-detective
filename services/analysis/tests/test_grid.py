from __future__ import annotations

import numpy as np
import pytest
from google.protobuf import json_format
from scipy import stats

from trenddetective.analysis.v1 import analysis_pb2, results_pb2
from trenddetective.common.v1 import common_pb2
from trenddetective.gateway.v1 import task_pb2
from trenddetective.preprocess.v1 import data_pb2

from conftest import ar1_noise, make_series, month_starts, series_plan
from analysis import grid, kendall, pipeline, regression
from analysis.errors import TaskFailure
from analysis.series import own_anomalies_cells
from analysis.storage import MemoryStorage

ORIGINAL = analysis_pb2.MANN_KENDALL_VARIANT_ORIGINAL
HAMED_RAO = analysis_pb2.MANN_KENDALL_VARIANT_HAMED_RAO
SEASONAL = analysis_pb2.MANN_KENDALL_VARIANT_SEASONAL
REGRESSION = analysis_pb2.GridTest(regression=analysis_pb2.Regression(correct_autocorrelation=True))


def mann_kendall(variant: int = 0) -> analysis_pb2.GridTest:
    return analysis_pb2.GridTest(mann_kendall=analysis_pb2.MannKendall(variant=variant))


def make_grid(rows: int, cols: int, steps: int, start: tuple[int, int] = (2000, 1)) -> data_pb2.Grid:
    g = data_pb2.Grid(
        variable_id="test-grid", units="mm/day", rows=rows, cols=cols, step=common_pb2.TIME_STEP_MONTHLY
    )
    g.lats.extend(np.linspace(10, 0, rows).tolist())  # north first
    g.lons.extend(np.linspace(0, 10, cols).tolist())  # west first
    g.bbox.CopyFrom(common_pb2.BBox(west=-0.5, south=-0.5, east=10.5, north=10.5))
    g.times.extend(month_starts(*start, steps))
    return g


def grid_plan(*tests: analysis_pb2.GridTest, fdr_q: float = 0.05) -> analysis_pb2.TestPlan:
    plan = analysis_pb2.TestPlan(alpha=0.05, fdr_q=fdr_q)
    plan.grid.extend(tests)
    return plan


def noise(steps: int, rows: int, cols: int, seed: int) -> np.ndarray:
    return np.random.default_rng(seed).normal(0, 1, (steps, rows, cols))


@pytest.mark.parametrize("variant", [ORIGINAL, HAMED_RAO, SEASONAL])
def test_each_cell_matches_the_series_tests(variant: int):
    rows, cols, steps = 3, 4, 144
    k = np.arange(steps)
    values = np.empty((steps, rows, cols))
    for r in range(rows):
        for c in range(cols):
            # Rounding to one decimal makes ties.
            values[:, r, c] = np.round(0.02 * (r - c) * k / 12 + ar1_noise(steps, 0.4, 1.0, seed=r * cols + c), 1)
    values[[5, 6, 50], 1, 2] = np.nan
    values[:30, 2, 3] = np.nan
    # The values serve as the anomalies, so the cells are tested as they are.
    out = grid.run(
        make_grid(rows, cols, steps), values, values, grid_plan(REGRESSION, mann_kendall(variant)),
        alpha=0.05, units="mm/day",
    )

    t, calendar = k / 12.0, k % 12
    flat = values.reshape(steps, -1)
    by_regression, by_kendall = out.layers
    for cell in range(rows * cols):
        y = flat[:, cell]
        fit = regression.fit(t, y, alpha=0.05, correct_autocorrelation=True)
        assert by_regression.slopes_per_decade[cell] == pytest.approx(fit.slope * 10, rel=1e-9)
        assert by_regression.p_values[cell] == pytest.approx(fit.p_value, rel=1e-7, abs=1e-12)
        mk = kendall.mann_kendall(y, t, calendar, variant=variant, alpha=0.05)
        assert by_kendall.slopes_per_decade[cell] == pytest.approx(mk.slope * 10, rel=1e-9)
        assert by_kendall.p_values[cell] == pytest.approx(mk.p_value, rel=1e-7, abs=1e-12)
    assert list(out.valid_steps) == np.isfinite(flat).sum(axis=0).tolist()


def test_fdr_matches_scipy():
    rng = np.random.default_rng(4)
    p = np.concatenate([rng.uniform(0, 1, 400), rng.uniform(0, 0.002, 30), np.full(5, np.nan)])
    significant, threshold = grid.benjamini_hochberg(p, 0.05)
    tested = np.isfinite(p)
    adjusted = stats.false_discovery_control(p[tested], method="bh")
    assert significant[tested].tolist() == (adjusted <= 0.05).tolist()
    assert not significant[~tested].any()
    assert threshold == p[tested][adjusted <= 0.05].max()
    # Nothing significant: no threshold.
    none, threshold = grid.benjamini_hochberg(np.array([0.5, 0.9, np.nan]), 0.05)
    assert not none.any() and threshold == 0.0


def test_a_trending_corner_shows_up_in_its_place():
    rows, cols, steps = 4, 5, 240
    values = noise(steps, rows, cols, seed=5)
    values[:, 0, 0] += 0.5 * np.arange(steps) / 12  # 5 per decade in the north-west corner
    g = make_grid(rows, cols, steps)
    out = grid.run(g, values, None, grid_plan(REGRESSION, mann_kendall()), alpha=0.05, units="mm/day")

    assert (out.rows, out.cols, out.fdr_q, out.units) == (rows, cols, 0.05, "mm/day")
    assert list(out.lats) == list(g.lats) and list(out.lons) == list(g.lons) and out.bbox == g.bbox
    # An unset variant runs, and is reported, as Hamed and Rao.
    assert out.layers[1].test.mann_kendall.variant == HAMED_RAO
    for layer in out.layers:
        # Row-major from the north-west corner: the trend is cell 0, and only there.
        assert layer.slopes_per_decade[0] == pytest.approx(5, abs=0.6)
        assert [i for i, s in enumerate(layer.significant) if s] == [0]
        assert layer.p_threshold == layer.p_values[0]


def test_cells_without_enough_data_are_left_out():
    rows, cols, steps = 2, 3, 120
    values = noise(steps, rows, cols, seed=6) + 0.3 * np.arange(steps)[:, None, None] / 12
    values[:, 0, 1] = np.nan  # sea in a land product
    values[:, 1, 0] = np.nan
    values[:10, 1, 0] = np.arange(10.0)  # ten values
    # The values serve as the anomalies: one value per calendar month would
    # give the cell no normals of its own, so no anomalies at all.
    out = grid.run(make_grid(rows, cols, steps), values, values, grid_plan(REGRESSION), alpha=0.05, units="mm/day")
    layer = out.layers[0]
    assert out.valid_steps[1] == 0 and out.valid_steps[3] == 10
    for cell in (1, 3):
        assert np.isnan(layer.slopes_per_decade[cell]) and np.isnan(layer.p_values[cell])
        assert not layer.significant[cell]
    # The correction counts only the cells that were tested.
    tested = np.array(layer.p_values)
    _, threshold = grid.benjamini_hochberg(tested, 0.05)
    assert layer.p_threshold == threshold


def test_own_anomalies_per_cell_are_the_values_less_the_seasons():
    # Checked against a least-squares fit with a constant per calendar month
    # and a straight line, the seasons taken at the mean time.
    steps = 96
    rng = np.random.default_rng(7)
    k = np.arange(steps)
    values = np.stack(
        [5 * np.sin(2 * np.pi * k / 12) + slope * k / 12 + rng.normal(0, 0.3, steps) for slope in (0.0, 1.0, -2.5)],
        axis=1,
    )
    values[[3, 15, 40], 1] = np.nan
    calendar = k % 12
    got = own_anomalies_cells(values, calendar)
    t = k / 12.0
    for c in range(values.shape[1]):
        ok = np.isfinite(values[:, c])
        design = np.column_stack([calendar == m for m in range(12)] + [t - t[ok].mean()]).astype(float)
        coef, *_ = np.linalg.lstsq(design[ok], values[ok, c], rcond=None)
        want = values[:, c] - coef[:12][calendar]
        assert np.allclose(got[:, c], want, equal_nan=True, atol=1e-9)


def put_cube(store: MemoryStorage, key: str, cube: np.ndarray) -> common_pb2.BlobRef:
    return store.put("prep", key, np.ascontiguousarray(cube, dtype="<f4").tobytes(), "application/octet-stream")


def gridded_task(store: MemoryStorage, with_anomalies: bool = True) -> analysis_pb2.AnalysisTask:
    rows, cols, steps = 2, 3, 240
    values = noise(steps, rows, cols, seed=8) + 0.4 * np.arange(steps)[:, None, None] / 12
    region = values.mean(axis=(1, 2))
    series_ref = store.put(
        "prep", "k/series.json", json_format.MessageToJson(make_series(region, start=(2000, 1))).encode(), "application/json"
    )
    g = make_grid(rows, cols, steps)
    g.values.CopyFrom(put_cube(store, "k/grid-values.f32", values))
    if with_anomalies:
        flat = values.reshape(steps, -1)
        anomalies = own_anomalies_cells(flat, np.arange(steps) % 12).reshape(values.shape)
        g.anomalies.CopyFrom(put_cube(store, "k/grid-anomalies.f32", anomalies))
    grid_ref = store.put("prep", "k/grid.json", json_format.MessageToJson(g).encode(), "application/json")
    plan = series_plan()
    plan.grid.extend([REGRESSION, mann_kendall()])
    task = analysis_pb2.AnalysisTask(tests=plan)
    task.input.series.CopyFrom(series_ref)
    task.input.grid.CopyFrom(grid_ref)
    return task


@pytest.mark.parametrize("with_anomalies", [True, False])
def test_pipeline_writes_the_grid_analysis(with_anomalies: bool):
    store = MemoryStorage()
    task = gridded_task(store, with_anomalies)
    result, cache_hit = pipeline.run(task, store)
    assert not cache_hit and result.HasField("grid")
    doc = json_format.Parse(store.get("analysis", result.grid.key), results_pb2.GridAnalysis())
    assert (doc.rows, doc.cols, len(doc.layers), len(doc.valid_steps)) == (2, 3, 2, 6)
    # 4 per decade everywhere, through noise.
    assert all(layer.significant == [True] * 6 for layer in doc.layers)
    assert store.puts[-1][1].endswith("/result.json")
    again, cache_hit = pipeline.run(task, store)
    assert cache_hit and again == result


def test_no_grid_tests_or_no_grid_means_no_grid_result():
    store = MemoryStorage()
    task = gridded_task(store)
    del task.tests.grid[:]
    assert not pipeline.run(task, store)[0].HasField("grid")

    store = MemoryStorage()
    task = gridded_task(store)
    task.input.ClearField("grid")  # a series-only source
    assert not pipeline.run(task, store)[0].HasField("grid")


def test_a_damaged_cube_is_our_bug():
    store = MemoryStorage()
    task = gridded_task(store)
    store.put("prep", "k/grid-values.f32", b"\x00" * 12, "application/octet-stream")
    with pytest.raises(TaskFailure) as info:
        pipeline.run(task, store)
    assert info.value.code == task_pb2.ERROR_CODE_INTERNAL


def test_the_false_discovery_rate_is_checked():
    with pytest.raises(TaskFailure) as info:
        grid.run(
            make_grid(2, 2, 60), noise(60, 2, 2, seed=1), None, grid_plan(REGRESSION, fdr_q=1.5),
            alpha=0.05, units="mm/day",
        )
    assert info.value.code == task_pb2.ERROR_CODE_INVALID_REQUEST
