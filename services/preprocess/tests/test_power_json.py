from __future__ import annotations

import json
from pathlib import Path

import numpy as np

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import data_pb2

from conftest import DELHI, raw_dataset
from preprocess.decoders import power_json


def test_delhi_bundle_gives_528_months_without_annual_rows():
    raw = raw_dataset(ingest_pb2.FORMAT_POWER_JSON, ["T2M"], ["power.json"], point=common_pb2.LatLon(lat=28.61, lon=77.21))
    dec = power_json.decode([DELHI], raw, "°C")

    times = dec.data["time"].values
    assert len(times) == 44 * 12
    assert str(times[0])[:10] == "1981-01-01"
    assert str(times[-1])[:10] == "2024-12-01"
    assert dec.step == common_pb2.TIME_STEP_MONTHLY
    assert not dec.gridded
    expected = json.loads(DELHI.read_text())["properties"]["parameter"]["T2M"]["198101"]
    assert dec.values.values[0] == expected
    # The file says "C". The task says "°C". Same unit, so no note.
    assert dec.units == "°C"
    assert dec.notes == []
    assert dec.point.lat == 28.61


def test_fill_values_become_missing_with_a_reason(tmp_path: Path):
    doc = {
        "geometry": {"coordinates": [10.0, 20.0, 5.0]},
        "properties": {"parameter": {"T2M": {"202001": 1.5, "202002": -999.0, "202003": 2.5, "202013": 2.0}}},
        "header": {"fill_value": -999.0},
        "parameters": {"T2M": {"units": "C"}},
    }
    path = tmp_path / "power.json"
    path.write_text(json.dumps(doc))
    raw = raw_dataset(ingest_pb2.FORMAT_POWER_JSON, ["T2M"], ["power.json"])
    dec = power_json.decode([path], raw, "")

    values = dec.values.values
    assert len(values) == 3
    assert np.isnan(values[1])
    assert dec.reasons.values[1] == data_pb2.MISSING_REASON_FILL_VALUE
    assert dec.units == "C"
    # The point comes from the GeoJSON when ingest didn't set one.
    assert (dec.point.lat, dec.point.lon) == (20.0, 10.0)
