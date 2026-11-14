"""Decoders turn raw files of one format into the canonical Decoded form.

Preprocess picks the decoder from RawDataset.format. Each decoder gets the
local paths of the dataset's files, in the order ingest listed them, and the
RawDataset itself for the variable names, the point and the dates.
"""

from __future__ import annotations

from collections.abc import Callable
from pathlib import Path

from trenddetective.ingest.v1 import ingest_pb2

from .. import errors
from ..canonical import Decoded
from . import giovanni_csv, netcdf, power_json, user_csv

Decoder = Callable[[list[Path], ingest_pb2.RawDataset, str], Decoded]

DECODERS: dict[int, Decoder] = {
    ingest_pb2.FORMAT_POWER_JSON: power_json.decode,
    ingest_pb2.FORMAT_NETCDF4: netcdf.decode,
    ingest_pb2.FORMAT_USER_CSV: user_csv.decode,
    ingest_pb2.FORMAT_GIOVANNI_CSV: giovanni_csv.decode,
    ingest_pb2.FORMAT_GIOVANNI_POINT_CSV: giovanni_csv.decode,
}


def for_format(fmt: int) -> Decoder:
    decoder = DECODERS.get(fmt)
    if decoder is None:
        name = ingest_pb2.Format.Name(fmt) if fmt in ingest_pb2.Format.values() else str(fmt)
        raise errors.unsupported(
            "This data format can't be prepared yet.",
            f"no decoder for {name}",
        )
    return decoder
