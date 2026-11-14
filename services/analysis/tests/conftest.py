from __future__ import annotations

import json
import math
from datetime import UTC, datetime
from pathlib import Path

import numpy as np
from google.protobuf import timestamp_pb2

from trenddetective.analysis.v1 import analysis_pb2
from trenddetective.common.v1 import common_pb2
from trenddetective.preprocess.v1 import data_pb2

REPO = Path(__file__).resolve().parents[3]
DELHI = REPO / "services" / "ingest" / "bundle" / "power-delhi-monthly" / "power.json"


def month_starts(year: int, month: int, count: int) -> list[timestamp_pb2.Timestamp]:
    out = []
    for k in range(count):
        dy, m0 = divmod(month - 1 + k, 12)
        ts = timestamp_pb2.Timestamp()
        ts.FromDatetime(datetime(year + dy, m0 + 1, 1, tzinfo=UTC))
        out.append(ts)
    return out


def make_series(
    values,
    *,
    start: tuple[int, int] = (1981, 1),
    units: str = "°C",
    step: int = common_pb2.TIME_STEP_MONTHLY,
    anomalies=None,
    anomaly_kind: int = data_pb2.ANOMALY_KIND_UNSPECIFIED,
    variable_id: str = "test-var",
) -> data_pb2.Series:
    values = np.asarray(values, dtype="float64")
    s = data_pb2.Series(variable_id=variable_id, units=units, step=step, anomaly_kind=anomaly_kind)
    s.times.extend(month_starts(*start, values.size))
    s.values.extend(values.tolist())
    s.coverage.extend(np.where(np.isnan(values), 0.0, 1.0).tolist())
    if anomalies is not None:
        s.anomalies.extend(np.asarray(anomalies, dtype="float64").tolist())
    return s


def ar1_noise(n: int, phi: float, sd: float, seed: int) -> np.ndarray:
    """AR(1) noise with lag-1 autocorrelation phi and standard deviation sd."""
    rng = np.random.default_rng(seed)
    e = np.empty(n)
    e[0] = rng.normal(0, sd)
    innovation = sd * math.sqrt(1 - phi**2)
    for i in range(1, n):
        e[i] = phi * e[i - 1] + rng.normal(0, innovation)
    return e


def seasonal_series(
    years: int = 44, *, per_decade: float = 0.3, amplitude: float = 8.0, sd: float = 0.8, phi: float = 0.5, seed: int = 1
) -> np.ndarray:
    """Monthly values like a temperature record: a seasonal cycle, a linear
    trend in units per decade and AR(1) noise."""
    n = years * 12
    k = np.arange(n)
    return 25.0 + amplitude * np.sin(2 * np.pi * (k % 12) / 12) + per_decade / 10 * k / 12 + ar1_noise(n, phi, sd, seed)


def delhi(parameter: str) -> data_pb2.Series:
    """A monthly series from the Delhi POWER bundle, the data of the offline
    demo. POWER's 13th month is the annual mean and is dropped."""
    doc = json.loads(DELHI.read_text())
    param = doc["properties"]["parameter"][parameter]
    fill = doc["header"]["fill_value"]
    keys = sorted(k for k in param if not k.endswith("13"))
    values = [math.nan if param[k] == fill else param[k] for k in keys]
    units = doc["parameters"][parameter]["units"] if "parameters" in doc else ""
    return make_series(values, start=(int(keys[0][:4]), int(keys[0][4:])), units=units, variable_id=parameter)


def series_plan(
    *,
    variant: int = analysis_pb2.MANN_KENDALL_VARIANT_HAMED_RAO,
    correct: bool = True,
    field: int = analysis_pb2.SERIES_FIELD_UNSPECIFIED,
    alpha: float = 0.05,
) -> analysis_pb2.TestPlan:
    plan = analysis_pb2.TestPlan(alpha=alpha, field=field)
    plan.series.add().regression.correct_autocorrelation = correct
    plan.series.add().mann_kendall.variant = variant
    return plan


def catalog_plan() -> analysis_pb2.TestPlan:
    """The catalog's default series tests (&series-tests in
    services/gateway/catalog.yaml), plus its grid tests."""
    plan = series_plan()
    plan.series.add().pettitt.SetInParent()
    plan.series.add().residual_checks.SetInParent()
    plan.series.add().outliers.robust_z_threshold = 3.5
    shuffle = plan.series.add().shuffle
    shuffle.permutations, shuffle.block_length = 1000, 12
    plan.series.add().trend_triangle.min_years = 10
    plan.series.add().monthly_trends.SetInParent()
    plan.series.add().years_to_significance.SetInParent()
    plan.grid.add().regression.correct_autocorrelation = True
    plan.fdr_q = 0.05
    return plan
