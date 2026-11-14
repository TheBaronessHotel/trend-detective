"""The worker's handling of one message, with NATS replaced by fakes. The
JetStream connection itself is checked end to end by make smoke."""

from __future__ import annotations

import asyncio
from types import SimpleNamespace

from google.protobuf import json_format

from trenddetective.gateway.v1 import task_pb2

from conftest import catalog_plan, delhi, make_series
from analysis.settings import Settings
from analysis.storage import MemoryStorage
from analysis.worker import SUBJECT_STAGE_DONE, Worker

TRACEPARENT = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"


class FakeMsg:
    def __init__(self, data: bytes, delivered: int = 1) -> None:
        self.data = data
        self.headers = {"traceparent": TRACEPARENT}
        self.metadata = SimpleNamespace(num_delivered=delivered)
        self.acked = self.termed = False
        self.nak_delay = None

    async def ack_sync(self):
        self.acked = True

    async def nak(self, delay=None):
        self.nak_delay = delay

    async def term(self):
        self.termed = True

    async def in_progress(self):
        pass


class FakeJetStream:
    def __init__(self) -> None:
        self.published = []

    async def publish(self, subject, payload, timeout=None, headers=None):
        self.published.append((subject, payload, headers))


def worker(store: MemoryStorage) -> Worker:
    return Worker(Settings("nats://x", "http://x", "a", "b"), store)


def task_message(store: MemoryStorage, series=None) -> bytes:
    ref = store.put("prep", "k/series.json", json_format.MessageToJson(series or delhi("T2M")).encode(), "application/json")
    task = task_pb2.Task()
    task.meta.job_id, task.meta.task_id, task.meta.stage = "job-1", "job-1-analysis-0", task_pb2.STAGE_ANALYSIS
    task.analysis.tests.CopyFrom(catalog_plan())
    task.analysis.input.series.CopyFrom(ref)
    return json_format.MessageToJson(task).encode()


def handle(store: MemoryStorage, msg: FakeMsg) -> FakeJetStream:
    js = FakeJetStream()
    asyncio.run(worker(store).handle(js, msg))
    return js


def stage_done(js: FakeJetStream) -> task_pb2.StageDone:
    (subject, payload, headers), = js.published
    assert subject == SUBJECT_STAGE_DONE
    assert headers == {"Nats-Msg-Id": "job-1-analysis-0.done", "traceparent": TRACEPARENT}
    return json_format.Parse(payload, task_pb2.StageDone())


def test_a_task_is_reported_then_acked():
    store = MemoryStorage()
    msg = FakeMsg(task_message(store))
    done = stage_done(handle(store, msg))
    assert msg.acked
    assert done.meta.task_id == "job-1-analysis-0"
    assert done.attempt == 1
    assert done.HasField("analysis") and done.analysis.series.bucket == "analysis"
    assert not done.HasField("error")


def test_a_failure_is_reported_with_a_reason():
    store = MemoryStorage()
    msg = FakeMsg(task_message(store, make_series([1.0] * 5)))
    done = stage_done(handle(store, msg))
    assert msg.acked
    assert done.error.code == task_pb2.ERROR_CODE_NO_DATA
    assert "too little data" in done.error.user_message


def test_a_storage_hiccup_is_retried_before_it_is_reported():
    store = MemoryStorage()
    data = task_message(store)
    del store.objects[("prep", "k/series.json")]

    first = FakeMsg(data, delivered=1)
    assert handle(store, first).published == []
    assert first.nak_delay == 10.0 and not first.acked

    last = FakeMsg(data, delivered=3)
    done = stage_done(handle(store, last))
    assert done.error.code == task_pb2.ERROR_CODE_INTERNAL and done.error.retryable


def test_garbage_is_dropped():
    msg = FakeMsg(b"not a task")
    assert handle(MemoryStorage(), msg).published == []
    assert msg.termed
