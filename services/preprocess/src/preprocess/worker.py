"""The JetStream consumer. Reads gateway.v1.Task messages from tasks.preprocess,
runs each through the pipeline, and publishes a StageDone on events.stage.done.

It follows ingest's worker:

- The durable consumer "preprocess" is created or updated at startup.
- A task is acked only after its StageDone is published. The StageDone uses
  Nats-Msg-Id "<task_id>.done", so a repeat publish is dropped.
- Transient failures, like a storage error, are NAKed with a backoff. After
  MAX_ATTEMPTS deliveries the failure is reported instead.
- While a task runs, a heartbeat tells JetStream it's still being worked on.

Preprocess never polls, so JetStream's delivery count is the attempt number.
Ingest needs a Redis counter only because its Harmony polls are deliveries
too.
"""

from __future__ import annotations

import asyncio
import logging
from datetime import UTC, datetime

import nats
from google.protobuf import json_format, timestamp_pb2
from nats.js.api import AckPolicy, ConsumerConfig

from trenddetective.gateway.v1 import task_pb2

from . import errors, pipeline
from .logs import TaskLog
from .settings import Settings
from .storage import Storage

STREAM = "TASKS"
DURABLE = "preprocess"
SUBJECT = "tasks.preprocess"
SUBJECT_STAGE_DONE = "events.stage.done"

MAX_ATTEMPTS = 3
BACKOFF = [10.0, 60.0]
FETCH_TIMEOUT = 5.0
PUBLISH_TIMEOUT = 5.0
DRAIN_TIMEOUT = 8.0

log = logging.getLogger("preprocess.worker")


class Worker:
    def __init__(self, settings: Settings, store: Storage) -> None:
        self.settings = settings
        self.store = store
        self._stop = asyncio.Event()
        self._running: set[asyncio.Task] = set()

    def stop(self) -> None:
        self._stop.set()

    async def run(self) -> None:
        nc = await nats.connect(self.settings.nats_url, name="preprocess", max_reconnect_attempts=-1)
        js = nc.jetstream()
        await js.add_consumer(
            STREAM,
            config=ConsumerConfig(
                durable_name=DURABLE,
                description="Preprocess tasks.",
                filter_subject=SUBJECT,
                ack_policy=AckPolicy.EXPLICIT,
                ack_wait=self.settings.ack_wait,
                max_deliver=MAX_ATTEMPTS,
                max_ack_pending=self.settings.concurrency,
            ),
        )
        sub = await js.pull_subscribe_bind(consumer=DURABLE, stream=STREAM)
        slots = asyncio.Semaphore(self.settings.concurrency)
        log.info("consuming", extra={"fields": {"subject": SUBJECT, "concurrency": self.settings.concurrency}})

        while not self._stop.is_set():
            await slots.acquire()
            try:
                msgs = await sub.fetch(1, timeout=FETCH_TIMEOUT)
            except nats.errors.TimeoutError:
                slots.release()
                continue
            except Exception as err:  # noqa: BLE001 - a broken fetch shouldn't kill the loop
                slots.release()
                log.warning("fetch failed", extra={"fields": {"error": str(err)}})
                await asyncio.sleep(1)
                continue
            for msg in msgs:
                t = asyncio.create_task(self._handle_and_release(js, msg, slots))
                self._running.add(t)
                t.add_done_callback(self._running.discard)

        if self._running:
            done, pending = await asyncio.wait(self._running, timeout=DRAIN_TIMEOUT)
            if pending:
                log.warning("stopped with tasks still running; JetStream will redeliver them")
        await nc.drain()

    async def _handle_and_release(self, js, msg, slots: asyncio.Semaphore) -> None:
        try:
            await self.handle(js, msg)
        finally:
            slots.release()

    async def handle(self, js, msg) -> None:
        task = task_pb2.Task()
        try:
            json_format.Parse(msg.data, task, ignore_unknown_fields=True)
        except json_format.ParseError as err:
            # Without a task ID there's nobody to report to.
            log.error("dropping a message that isn't a valid Task", extra={"fields": {"error": str(err)}})
            await msg.term()
            return

        meta = task.meta
        headers = msg.headers or {}
        fields = {"job_id": meta.job_id, "task_id": meta.task_id}
        traceparent = headers.get("traceparent", "")
        if traceparent.count("-") == 3:
            fields["trace_id"] = traceparent.split("-")[1]
        tlog = TaskLog(log, fields)
        attempt = msg.metadata.num_delivered if msg.metadata else 1

        report = task_pb2.StageDone(attempt=attempt, finished_at=_now())
        report.meta.CopyFrom(meta)

        heartbeat = asyncio.create_task(_heartbeat(msg, self.settings.ack_wait / 3))
        try:
            if not task.HasField("preprocess"):
                raise errors.invalid("The job couldn't start.", f"task on {SUBJECT} has no preprocess spec")
            result, cache_hit = await asyncio.to_thread(pipeline.run, task.preprocess, self.store, tlog)
            report.cache_hit = cache_hit
            report.preprocess.CopyFrom(result)
            tlog.info("preprocessed", extra={"fields": {"cache_hit": cache_hit, "attempt": attempt}})
        except Exception as exc:  # noqa: BLE001 - every failure becomes a report
            failure = errors.as_failure(exc)
            if failure.transient and attempt < MAX_ATTEMPTS:
                delay = BACKOFF[min(attempt - 1, len(BACKOFF) - 1)]
                tlog.warning("retrying", extra={"fields": {"error": str(exc), "attempt": attempt, "delay_s": delay}})
                await msg.nak(delay=delay)
                return
            level = logging.ERROR if failure.code == task_pb2.ERROR_CODE_INTERNAL else logging.WARNING
            tlog.log(
                level,
                "task failed",
                exc_info=failure.code == task_pb2.ERROR_CODE_INTERNAL,
                extra={"fields": {"error": failure.detail or str(exc), "code": task_pb2.ErrorCode.Name(failure.code), "attempt": attempt}},
            )
            report.error.CopyFrom(failure.task_error())
        finally:
            heartbeat.cancel()

        report.finished_at.CopyFrom(_now())
        await self._finish(js, msg, report, traceparent, tlog)

    async def _finish(self, js, msg, report: task_pb2.StageDone, traceparent: str, tlog) -> None:
        headers = {"Nats-Msg-Id": f"{report.meta.task_id}.done"}
        if traceparent:
            headers["traceparent"] = traceparent
        try:
            await js.publish(
                SUBJECT_STAGE_DONE,
                json_format.MessageToJson(report, indent=None).encode("utf-8"),
                timeout=PUBLISH_TIMEOUT,
                headers=headers,
            )
        except Exception as err:  # noqa: BLE001
            tlog.error("couldn't publish StageDone, will retry", extra={"fields": {"error": str(err)}})
            await msg.nak(delay=BACKOFF[0])
            return
        try:
            await msg.ack_sync()
        except Exception as err:  # noqa: BLE001
            # The report is out, so a redelivery only repeats it, and the
            # gateway counts each task once.
            tlog.warning("ack failed after reporting", extra={"fields": {"error": str(err)}})


async def _heartbeat(msg, interval: float) -> None:
    while True:
        await asyncio.sleep(interval)
        try:
            await msg.in_progress()
        except Exception:  # noqa: BLE001 - the next beat or the ack will tell
            pass


def _now() -> timestamp_pb2.Timestamp:
    ts = timestamp_pb2.Timestamp()
    ts.FromDatetime(datetime.now(UTC))
    return ts
