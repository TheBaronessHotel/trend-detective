"""Cheap checks on the parts the other tests don't run: the entry point and
the settings. A missing dependency or a renamed module would otherwise only
show up when the Docker image starts."""

from __future__ import annotations

import importlib

import pytest

from analysis.settings import Settings


def test_entry_point_and_storage_import():
    for module in ("analysis.worker", "analysis.__main__", "analysis.storage"):
        importlib.import_module(module)


def test_settings_from_env(monkeypatch):
    for name, value in {
        "NATS_URL": "nats://nats:4222",
        "S3_ENDPOINT": "http://seaweedfs:8333",
        "S3_ACCESS_KEY": "a",
        "S3_SECRET_KEY": "b",
        "ANALYSIS_ACK_WAIT": "90s",
    }.items():
        monkeypatch.setenv(name, value)
    monkeypatch.delenv("ANALYSIS_CONCURRENCY", raising=False)
    s = Settings.from_env()
    assert s.ack_wait == 90
    assert s.concurrency == 2


def test_settings_name_what_is_missing(monkeypatch):
    for name in ("NATS_URL", "S3_ENDPOINT", "S3_ACCESS_KEY", "S3_SECRET_KEY"):
        monkeypatch.delenv(name, raising=False)
    with pytest.raises(ValueError, match="NATS_URL"):
        Settings.from_env()
