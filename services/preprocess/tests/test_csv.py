from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2

from trenddetective.preprocess.v1 import data_pb2

from conftest import raw_dataset
from preprocess import decoders, errors
from preprocess.decoders import giovanni_csv, user_csv


def test_user_csv_with_month_dates(tmp_path: Path):
    path = tmp_path / "upload.csv"
    path.write_text("date,value\n2001-01,3.5\n2001-02,\n2001-03,4\n")
    raw = raw_dataset(ingest_pb2.FORMAT_USER_CSV, [], ["upload.csv"])

    dec = user_csv.decode([path], raw, "mm/day")

    assert [str(t)[:10] for t in dec.data["time"].values] == ["2001-01-01", "2001-02-01", "2001-03-01"]
    assert dec.values.values[0] == 3.5
    assert np.isnan(dec.values.values[1])
    assert dec.step == common_pb2.TIME_STEP_MONTHLY


def test_user_csv_finds_the_columns_in_any_order(tmp_path: Path):
    path = tmp_path / "upload.csv"
    path.write_text("site;value;when\nA;1.0;2020-01-15\nA;2.0;2020-01-16\n")
    raw = raw_dataset(ingest_pb2.FORMAT_USER_CSV, [], ["upload.csv"])

    dec = user_csv.decode([path], raw, "")

    assert dec.step == common_pb2.TIME_STEP_DAILY
    assert list(dec.values.values) == [1.0, 2.0]


def test_user_csv_without_dates_is_refused(tmp_path: Path):
    path = tmp_path / "upload.csv"
    path.write_text("a,b\n1,2\n")
    raw = raw_dataset(ingest_pb2.FORMAT_USER_CSV, [], ["upload.csv"])
    with pytest.raises(errors.TaskFailure):
        user_csv.decode([path], raw, "")


# The layouts services/ingest/README.md describes for giovanni-area.
GIOVANNI_AREA = """\
Title:,Area-averaged time series
Units:,1/cm^2
User Start Date:,2020-01-01T00:00:00Z

time,mean_OMNO2d_003_ColumnAmountNO2TropCloudScreened
2020-01-01T00:00:00,1.25
2020-01-02 00:00:00,
2020-01-03T00:00:00,0.5
2020-01-04T00:00:00,2
"""

# The sample from services/ingest/internal/giovanni/giovanni_test.go, what the
# giovanni-point adapter stores as point.csv.
GIOVANNI_POINT = """\
prod_name,GLDAS_NOAH025_M.2.1
doi,10.5067/SXAVCZFAQLNO
param_short_name,SoilMoi0_10cm_inst
unit,kg m-2
undef,-9999
begin_time,2010-01-01 00:00:00
end_time,2010-03-01 00:00:00
lat,30.625
lon,75.625
lat_resolution,0.25
lon_resolution,0.25
mean,9.17e+00

Timestamp (UTC),Data
2010-01-01 00:00,9.32422
2010-02-01 00:00,-9999
2010-03-01 00:00,7.34181
"""


def test_giovanni_area_average(tmp_path: Path):
    path = tmp_path / "area.csv"
    path.write_text(GIOVANNI_AREA)
    raw = raw_dataset(ingest_pb2.FORMAT_GIOVANNI_CSV, ["OMNO2d_003_ColumnAmountNO2TropCloudScreened"], ["area.csv"])

    dec = decoders.for_format(raw.format)([path], raw, "")

    assert dec.step == common_pb2.TIME_STEP_DAILY
    assert len(dec.data["time"]) == 4
    assert np.isnan(dec.values.values[1])
    assert dec.values.values[3] == 2.0
    # "Units:" with its colon is still found.
    assert dec.units == "1/cm^2"


def test_giovanni_point_reads_units_point_and_undef(tmp_path: Path):
    path = tmp_path / "point.csv"
    path.write_text(GIOVANNI_POINT)
    raw = raw_dataset(ingest_pb2.FORMAT_GIOVANNI_POINT_CSV, ["GLDAS_NOAH025_M_2_1_SoilMoi0_10cm_inst"], ["point.csv"])

    dec = decoders.for_format(raw.format)([path], raw, "kg/m²")

    assert dec.units == "kg/m²"
    assert dec.notes == []  # "kg m-2" and "kg/m²" are the same unit
    assert (dec.point.lat, dec.point.lon) == (30.625, 75.625)
    assert dec.step == common_pb2.TIME_STEP_MONTHLY
    assert [str(t)[:10] for t in dec.data["time"].values] == ["2010-01-01", "2010-02-01", "2010-03-01"]
    assert dec.values.values[0] == 9.32422
    assert np.isnan(dec.values.values[1])
    assert dec.reasons.values[1] == data_pb2.MISSING_REASON_FILL_VALUE


def test_giovanni_point_with_a_huge_undef(tmp_path: Path):
    path = tmp_path / "point.csv"
    path.write_text("unit,mm/day\nundef,-1.26765e+30\n\nTimestamp (UTC),Data\n2020-01-01 00:00,-1.26765e+30\n2020-01-02 00:00,3\n")
    raw = raw_dataset(ingest_pb2.FORMAT_GIOVANNI_POINT_CSV, ["X"], ["point.csv"])

    dec = giovanni_csv.decode([path], raw, "")

    assert np.isnan(dec.values.values[0])
    assert dec.values.values[1] == 3.0
