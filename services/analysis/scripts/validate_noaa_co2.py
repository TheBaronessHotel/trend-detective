"""Checks analysis against a published result: the trend of Mauna Loa CO2
against NOAA's own annual increase, over several windows. It downloads NOAA's
current files, so it needs the internet:

    uv run python scripts/validate_noaa_co2.py

Or give it a folder that already holds co2_mm_mlo.txt and co2_gr_mlo.txt from
gml.noaa.gov/webdata/ccgg/trends/co2/, for example when a proxy is in the way:

    uv run python scripts/validate_noaa_co2.py --from path/to/folder

tests/test_validation.py runs the same checks offline on a fixture.

The columns, all in ppm a year:

- NOAA: the mean of NOAA's published annual increases, Jan 1 to Dec 31.
- line: the regression's slope on NOAA's deseasonalized values. A straight
  line weighs the middle of the window most, so where growth speeds up, it
  comes out below the mean of the yearly increases.
- ours: the regression's slope on analysis's own anomalies, whose normals
  come with a trend, the same as preprocess's.
- plain: the slope on anomalies against each calendar month's plain mean,
  the way normals were taken before ANA-5.
- expected: what plain should be, line * 144 (Y^2 - 1) / (144 Y^2 - 1).
- seasonal: Sen's slope of Seasonal Kendall.
"""

from __future__ import annotations

import argparse
import sys
import urllib.request
from datetime import UTC, datetime
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path[:0] = [str(ROOT / "services" / "analysis" / "src"), str(ROOT / "gen" / "python")]

import numpy as np  # noqa: E402
from google.protobuf import timestamp_pb2  # noqa: E402

from analysis import suite  # noqa: E402
from trenddetective.analysis.v1 import analysis_pb2  # noqa: E402
from trenddetective.common.v1 import common_pb2  # noqa: E402
from trenddetective.preprocess.v1 import data_pb2  # noqa: E402

BASE = "https://gml.noaa.gov/webdata/ccgg/trends/co2/"
WINDOWS = [(2010, 2019), (2015, 2021), (2015, 2024), (2000, 2024), (1981, 2024)]


def table(name: str, folder: Path | None) -> list[list[str]]:
    if folder is not None:
        text = (folder / name).read_text(encoding="utf-8")
    else:
        with urllib.request.urlopen(BASE + name, timeout=30) as resp:
            text = resp.read().decode("utf-8")
    return [line.split() for line in text.splitlines() if line.strip() and not line.startswith("#")]


def series(values: np.ndarray, first: int, anomalies: np.ndarray | None = None) -> data_pb2.Series:
    s = data_pb2.Series(variable_id="co2-mauna-loa", units="ppm", step=common_pb2.TIME_STEP_MONTHLY)
    for k in range(values.size):
        ts = timestamp_pb2.Timestamp()
        ts.FromDatetime(datetime(first + k // 12, k % 12 + 1, 1, tzinfo=UTC))
        s.times.append(ts)
    s.values.extend(values.tolist())
    if anomalies is not None:
        s.anomalies.extend(anomalies.tolist())
    return s


def plan(variant: int) -> analysis_pb2.TestPlan:
    p = analysis_pb2.TestPlan()
    p.series.add().regression.correct_autocorrelation = True
    p.series.add().mann_kendall.variant = variant
    return p


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--from", dest="folder", type=Path, help="read NOAA's files from this folder instead")
    folder = parser.parse_args().folder
    monthly = {(int(r[0]), int(r[1])): (float(r[3]), float(r[4])) for r in table("co2_mm_mlo.txt", folder)}
    growth = {int(r[0]): float(r[1]) for r in table("co2_gr_mlo.txt", folder)}
    hamed_rao = plan(analysis_pb2.MANN_KENDALL_VARIANT_HAMED_RAO)
    print(f"{'window':<10} {'NOAA':>6} {'line':>6} {'ours':>6} {'plain':>6} {'expected':>9} {'seasonal':>9}")
    for first, last in WINDOWS:
        if any(y not in growth for y in range(first, last + 1)):
            continue
        keys = [(y, m) for y in range(first, last + 1) for m in range(1, 13)]
        average = np.array([monthly[k][0] for k in keys])
        deseasonalized = np.array([monthly[k][1] for k in keys])
        month = np.arange(average.size) % 12
        plain_anomalies = average - np.array([average[month == m].mean() for m in range(12)])[month]
        published = float(np.mean([growth[y] for y in range(first, last + 1)]))

        line = suite.run(series(average, first, deseasonalized - deseasonalized.mean()), hamed_rao).regression
        ours = suite.run(series(average, first), hamed_rao).regression
        plain = suite.run(series(average, first, plain_anomalies), hamed_rao).regression
        seasonal = suite.run(series(average, first), plan(analysis_pb2.MANN_KENDALL_VARIANT_SEASONAL)).mann_kendall
        years = last - first + 1
        expected = line.slope_per_decade / 10 * 144 * (years**2 - 1) / (144 * years**2 - 1)
        print(
            f"{first}-{last:<5} {published:6.3f} {line.slope_per_decade / 10:6.3f} {ours.slope_per_decade / 10:6.3f} "
            f"{plain.slope_per_decade / 10:6.3f} {expected:9.3f} {seasonal.sens_slope_per_decade / 10:9.3f}"
        )
    return 0


if __name__ == "__main__":
    sys.exit(main())
