from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2

from conftest import raw_dataset
from preprocess import errors
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


GIOVANNI_AREA = """\
Title:,Area-averaged time series
User Start Date:,2020-01-01T00:00:00Z
User End Date:,2020-01-04T23:59:59Z
,
time,mean_GPM_3IMERGDF_07_precipitation
2020-01-01T00:00:00,1.25
2020-01-02 00:00:00,
2020-01-03T00:00:00,0.5
2020-01-04T00:00:00,2
"""

GIOVANNI_POINT = """\
prod_name,GLDAS_NOAH025_M_2_1
param_short_name,SoilMoi0_10cm_inst
param_name,SoilMoi0_10cm_inst
unit,kg m-2
begin_time,2020-01-01T00:00:00
end_time,2020-03-31T23:59:59
lat,28.6
lon,77.2
Timestamp,SoilMoi0_10cm_inst
2020-01-01T00:00:00,20.1
2020-02-01T00:00:00,19.5
2020-03-01T00:00:00,18.0
"""


def test_giovanni_area_average(tmp_path: Path):
    path = tmp_path / "area.csv"
    path.write_text(GIOVANNI_AREA)
    raw = raw_dataset(ingest_pb2.FORMAT_GIOVANNI_CSV, ["GPM_3IMERGDF_07_precipitation"], ["area.csv"])

    dec = giovanni_csv.decode([path], raw, "mm/day")

    assert dec.step == common_pb2.TIME_STEP_DAILY
    assert len(dec.data["time"]) == 4
    assert np.isnan(dec.values.values[1])
    assert dec.values.values[3] == 2.0


def test_giovanni_point_reads_units_and_point(tmp_path: Path):
    path = tmp_path / "point.csv"
    path.write_text(GIOVANNI_POINT)
    raw = raw_dataset(ingest_pb2.FORMAT_GIOVANNI_CSV, ["GLDAS_NOAH025_M_2_1_SoilMoi0_10cm_inst"], ["point.csv"])

    dec = giovanni_csv.decode([path], raw, "")

    assert dec.units == "kg m-2"
    assert (dec.point.lat, dec.point.lon) == (28.6, 77.2)
    assert dec.step == common_pb2.TIME_STEP_MONTHLY
    assert list(dec.values.values) == [20.1, 19.5, 18.0]
