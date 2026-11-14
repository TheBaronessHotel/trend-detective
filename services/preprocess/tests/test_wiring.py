"""Cheap checks on the parts the unit tests don't run: the worker, the entry
point and the settings. A missing dependency or a renamed module would
otherwise only show up when the Docker image starts."""

from __future__ import annotations

import importlib

import pytest

from trenddetective.ingest.v1 import ingest_pb2

from preprocess import decoders
from preprocess.settings import Settings

# Formats some ingest adapter can produce today. GeoTIFF arrives with the
# appeears adapter, which is marked Later.
NOT_YET = {ingest_pb2.FORMAT_UNSPECIFIED, ingest_pb2.FORMAT_GEOTIFF}


def test_worker_and_entry_point_import():
    for module in ("preprocess.worker", "preprocess.__main__", "preprocess.storage"):
        importlib.import_module(module)


@pytest.mark.parametrize("fmt", sorted(set(ingest_pb2.Format.values()) - NOT_YET))
def test_every_ingest_format_has_a_decoder(fmt):
    assert decoders.for_format(fmt) is not None


def test_settings_from_env(monkeypatch):
    for name, value in {
        "NATS_URL": "nats://nats:4222",
        "S3_ENDPOINT": "http://seaweedfs:8333",
        "S3_ACCESS_KEY": "a",
        "S3_SECRET_KEY": "b",
        "PREPROCESS_ACK_WAIT": "90s",
    }.items():
        monkeypatch.setenv(name, value)
    s = Settings.from_env()
    assert s.ack_wait == 90
    assert s.concurrency == 2


def test_settings_name_what_is_missing(monkeypatch):
    for name in ("NATS_URL", "S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY"):
        monkeypatch.delenv(name, raising=False)
    with pytest.raises(ValueError, match="NATS_URL"):
        Settings.from_env()
