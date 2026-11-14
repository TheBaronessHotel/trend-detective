"""Our numbers against a published result: the trend of Mauna Loa CO2 against
NOAA's own annual increase. The data are a small NOAA fixture, see
tests/data/noaa_mlo_co2_2010_2021.csv. scripts/validate_noaa_co2.py runs the
same comparison on NOAA's current files and longer windows."""

from __future__ import annotations

import csv
from pathlib import Path

import numpy as np
import pytest

from trenddetective.analysis.v1 import analysis_pb2

from conftest import make_series, series_plan
from analysis import suite

FIXTURE = Path(__file__).parent / "data" / "noaa_mlo_co2_2010_2021.csv"
HAMED_RAO = analysis_pb2.MANN_KENDALL_VARIANT_HAMED_RAO
SEASONAL = analysis_pb2.MANN_KENDALL_VARIANT_SEASONAL


def noaa(first: int, last: int) -> tuple[np.ndarray, np.ndarray, float]:
    """Monthly averages and NOAA's deseasonalized values for whole years, and
    the mean of NOAA's published annual increases over those years."""
    with FIXTURE.open() as f:
        rows = [r for r in csv.DictReader(line for line in f if not line.startswith("#"))]
    rows = [r for r in rows if first <= int(r["year"]) <= last]
    average = np.array([float(r["average"]) for r in rows])
    deseasonalized = np.array([float(r["deseasonalized"]) for r in rows])
    increases = [float(r["annual_increase"]) for r in rows if r["annual_increase"]]
    assert len(average) == 12 * len(increases) == 12 * (last - first + 1)
    return average, deseasonalized, float(np.mean(increases))


def run(values: np.ndarray, first: int, *, anomalies=None, variant: int = HAMED_RAO):
    s = make_series(values, start=(first, 1), units="ppm", anomalies=anomalies)
    return suite.run(s, series_plan(variant=variant))


def per_year(values: np.ndarray, first: int, *, anomalies=None, variant: int = HAMED_RAO) -> tuple[float, float]:
    """The regression's slope and Sen's slope, in ppm a year."""
    out = run(values, first, anomalies=anomalies, variant=variant)
    return out.regression.slope_per_decade / 10, out.mann_kendall.sens_slope_per_decade / 10


def plain_monthly_anomalies(values: np.ndarray) -> np.ndarray:
    """Each value minus the plain mean of its calendar month, the way normals
    were taken before ANA-5."""
    month = np.arange(values.size) % 12
    return values - np.array([values[month == m].mean() for m in range(12)])[month]


def test_a_steady_decade_grows_at_noaas_published_rate():
    # CO2 grew steadily in 2010-2019, so a straight line through NOAA's
    # deseasonalized values and the mean of its annual increases measure the
    # same thing.
    average, deseasonalized, published = noaa(2010, 2019)
    assert published == pytest.approx(2.425, abs=0.001)
    regression, sen = per_year(average, 2010, anomalies=deseasonalized - deseasonalized.mean())
    assert regression == pytest.approx(published, rel=0.005)
    assert sen == pytest.approx(published, rel=0.005)


@pytest.mark.parametrize("first, last", [(2010, 2019), (2015, 2021)])
def test_seasonal_kendall_keeps_the_whole_trend(first: int, last: int):
    # It compares each month only with the same month in other years, so the
    # way the seasons are taken out doesn't touch its slope.
    average, deseasonalized, _ = noaa(first, last)
    reference, _ = per_year(average, first, anomalies=deseasonalized - deseasonalized.mean())
    _, seasonal = per_year(average, first, variant=SEASONAL)
    assert seasonal == pytest.approx(reference, rel=0.005)


@pytest.mark.parametrize("first, last", [(2010, 2019), (2015, 2021)])
def test_our_anomalies_keep_the_whole_trend(first: int, last: int):
    # Analysis's own anomalies, with normals taken along with a trend, give
    # the same line as NOAA's deseasonalized values, and no sawtooth around it.
    average, deseasonalized, _ = noaa(first, last)
    reference = run(average, first, anomalies=deseasonalized - deseasonalized.mean()).regression
    ours = run(average, first).regression
    assert ours.slope_per_decade == pytest.approx(reference.slope_per_decade, rel=0.003)
    assert ours.model.residual_sd < 1.1 * reference.model.residual_sd


@pytest.mark.parametrize("first, last", [(2010, 2019), (2015, 2021)])
def test_plain_monthly_normals_would_lose_part_of_the_trend(first: int, last: int):
    # Why the normals come with a trend. A plain mean of each calendar month
    # takes in the trend's rise within the year, so the anomalies keep only a
    # staircase, whose slope is the true one times 144 (Y^2 - 1) / (144 Y^2 - 1)
    # for Y whole years: 1% low over 10 years, 2% over 7, the CO2 demo's span.
    # The staircase also doubles the scatter around the line.
    average, deseasonalized, _ = noaa(first, last)
    reference = run(average, first, anomalies=deseasonalized - deseasonalized.mean()).regression
    plain = run(average, first, anomalies=plain_monthly_anomalies(average)).regression
    years = last - first + 1
    ratio = plain.slope_per_decade / reference.slope_per_decade
    assert ratio == pytest.approx(144 * (years**2 - 1) / (144 * years**2 - 1), abs=0.003)
    assert plain.model.residual_sd > 1.8 * reference.model.residual_sd
