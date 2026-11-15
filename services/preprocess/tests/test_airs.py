"""AIRS3STM v7 through the real file layout Harmony returns.

tests/fixtures/airs3stm-2020-02-delhi.nc4 is a trimmed copy of a HOSS subset
fetched on 2026-11-15 (February 2020, 27-29 N, 76-78 E): the root's 1-D
coordinate variables, which the data group lacks, and seven variables from
ascending/Data_Fields with their original dims, attributes and fill values.
Only the unused groups, like location/Data_Fields, were dropped.
"""

from __future__ import annotations

from pathlib import Path

import numpy as np
import pytest
from google.protobuf import json_format

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2
from trenddetective.preprocess.v1 import preprocess_pb2

from preprocess import filters
from preprocess.decoders import netcdf

FIXTURE = Path(__file__).parent / "fixtures" / "airs3stm-2020-02-delhi.nc4"
GROUP = "ascending/Data_Fields/"
PS = GROUP + "SurfPres_Forecast_A"


def raw(*names: str) -> ingest_pb2.RawDataset:
    r = ingest_pb2.RawDataset(format=ingest_pb2.FORMAT_NETCDF4, variables=[GROUP + n for n in names])
    # AIRS granules have no time axis; ingest records each file's dates.
    r.files.add().coverage.CopyFrom(
        common_pb2.DateRange(start=common_pb2.Date(year=2020, month=2, day=1), end=common_pb2.Date(year=2020, month=2, day=29))
    )
    return r


@pytest.mark.parametrize(
    ("name", "lo", "hi"),
    [
        ("TotH2OVap_A", 10, 20),  # kg/m², the same as mm of water
        ("SurfAirTemp_A", 285, 305),  # K
        ("SurfSkinTemp_A", 290, 310),  # K
        ("CloudFrc_A", 0, 1),
        ("TotO3_A", 250, 350),  # DU
    ],
)
def test_maps_find_their_coordinates_at_the_root(name, lo, hi):
    dec = netcdf.decode([FIXTURE], raw(name), "")
    assert dec.values.dims == ("time", "lat", "lon")
    assert list(dec.data["lat"].values) == [29.0, 28.0, 27.0]  # north first
    assert list(dec.data["lon"].values) == [76.0, 77.0, 78.0]
    assert str(dec.data["time"].values[0])[:10] == "2020-02-01"
    v = dec.values.values
    assert np.isfinite(v).all() and lo < v.min() and v.max() < hi


def test_methane_keeps_its_levels_and_reads_surface_pressure():
    dec = netcdf.decode([FIXTURE], raw("CH4_VMR_A", "SurfPres_Forecast_A"), "mol/mol")
    assert dec.values.dims == ("time", "level", "lat", "lon")
    assert dec.data.sizes["level"] == 24
    assert dec.data["level"].values[0] == 1000.0 and dec.data["level"].values[-1] == 1.0
    ps = dec.data["helper:" + PS].values
    assert ps.shape == (1, 3, 3) and 950 < ps.min() and ps.max() < 1010


def test_methane_column_average_from_the_real_file():
    dec = netcdf.decode([FIXTURE], raw("CH4_VMR_A", "SurfPres_Forecast_A"), "mol/mol")
    chain = json_format.ParseDict(
        {
            "ops": [
                {"qc_range": {"min": 1e-7, "max": 5e-6}},
                {"column_average": {"surface_pressure": PS}},
                {"linear": {"scale": 1e9, "units": "ppb"}},
            ]
        },
        preprocess_pb2.FilterChain(),
    )
    filters.apply(dec, chain)
    v = dec.values.values
    assert dec.values.dims == ("time", "lat", "lon")
    assert dec.units == "ppb"
    # Below the ~1870 ppb near the ground, because the column includes the
    # stratosphere, where methane falls to ~720 ppb at 1 hPa.
    assert np.isfinite(v).all() and 1800 < v.min() and v.max() < 1860


def test_an_unknown_variable_names_what_the_group_has():
    with pytest.raises(Exception) as info:
        netcdf.decode([FIXTURE], raw("Nope_A"), "")
    assert "TotH2OVap_A" in getattr(info.value, "detail", "")
