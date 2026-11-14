from __future__ import annotations

from pathlib import Path

import numpy as np
import pandas as pd
import pytest
import xarray as xr

from trenddetective.common.v1 import common_pb2
from trenddetective.ingest.v1 import ingest_pb2

REPO = Path(__file__).resolve().parents[3]
DELHI = REPO / "services" / "ingest" / "bundle" / "power-delhi-monthly" / "power.json"


def date_range(start: str, end: str) -> common_pb2.DateRange:
    s, e = pd.Timestamp(start), pd.Timestamp(end)
    return common_pb2.DateRange(
        start=common_pb2.Date(year=s.year, month=s.month, day=s.day),
        end=common_pb2.Date(year=e.year, month=e.month, day=e.day),
    )


def raw_dataset(fmt: int, variables: list[str], keys: list[str], **kw) -> ingest_pb2.RawDataset:
    raw = ingest_pb2.RawDataset(format=fmt, variables=variables, variable_id=kw.pop("variable_id", "test-var"), **kw)
    for key in keys:
        raw.files.add().blob.CopyFrom(common_pb2.BlobRef(bucket="raw", key=key))
    return raw


def write_imerg_like(path: Path, months: list[str], lats: np.ndarray, lons: np.ndarray, fill_at=None) -> None:
    """A file shaped like a HOSS subset of GPM IMERG monthly: the variable in
    group Grid, stored (time, lon, lat), latitudes south first, fill -9999.9."""
    times = pd.to_datetime(months).values
    data = np.arange(len(times) * len(lons) * len(lats), dtype="float32").reshape(len(times), len(lons), len(lats)) / 10
    if fill_at is not None:
        data[fill_at] = -9999.9
    da = xr.DataArray(
        data,
        dims=("time", "lon", "lat"),
        coords={"time": times, "lon": lons, "lat": lats},
        attrs={"units": "mm/hr"},
        name="precipitation",
    )
    da.encoding["_FillValue"] = np.float32(-9999.9)
    da.to_dataset().to_netcdf(path, group="Grid", engine="netcdf4")


@pytest.fixture
def delhi_bytes() -> bytes:
    return DELHI.read_bytes()
