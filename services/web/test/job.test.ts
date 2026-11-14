import { describe, expect, test } from "bun:test";
import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { JobSchema, JobState, TaskState } from "@gen/gateway/v1/gateway_pb";
import { Stage } from "@gen/gateway/v1/task_pb";
import { isFinished, stageViews } from "../src/lib/job";

function job(tasks: { stage: Stage; state: TaskState; cacheHit?: boolean; message?: string; ms?: [number, number] }[]) {
  return create(JobSchema, {
    state: JobState.RUNNING,
    tasks: tasks.map((t) => ({
      meta: { stage: t.stage },
      state: t.state,
      cacheHit: t.cacheHit ?? false,
      error: t.message ? { userMessage: t.message, retryable: false } : undefined,
      startedAt: t.ms ? timestampFromMs(t.ms[0]) : undefined,
      finishedAt: t.ms ? timestampFromMs(t.ms[1]) : undefined,
    })),
  });
}

describe("stageViews", () => {
  test("only the stages up to stopAfter", () => {
    const views = stageViews(job([]), Stage.INGEST);
    expect(views.map((v) => v.stage)).toEqual([Stage.INGEST]);
    expect(views[0]?.state).toBe("waiting");
  });

  test("a finished ingest, from the cache", () => {
    const [ingest, prep] = stageViews(
      job([{ stage: Stage.INGEST, state: TaskState.SUCCEEDED, cacheHit: true, ms: [1_000, 3_500] }]),
      Stage.PREPROCESS,
    );
    expect(ingest).toMatchObject({ state: "done", cached: true, seconds: 2.5 });
    expect(prep?.state).toBe("waiting");
  });

  test("a failed task's message reaches the user", () => {
    const [ingest] = stageViews(
      job([{ stage: Stage.INGEST, state: TaskState.FAILED, message: 'Row 3: "abc" isn\'t a number.' }]),
      Stage.INGEST,
    );
    expect(ingest).toMatchObject({ state: "failed", error: 'Row 3: "abc" isn\'t a number.' });
  });

  // The gateway only hears about a task when it finishes, so queued may
  // already mean running, as with a Harmony fetch that takes minutes.
  test("a queued task counts as running, since the job was created", () => {
    const j = job([{ stage: Stage.INGEST, state: TaskState.QUEUED }]);
    j.createdAt = timestampFromMs(Date.UTC(2026, 10, 14, 19, 43, 30));
    const [ingest] = stageViews(j, Stage.PREPROCESS);
    expect(ingest).toMatchObject({ state: "running", since: Date.UTC(2026, 10, 14, 19, 43, 30) });
  });

  test("the next stage runs from when the one before finished", () => {
    const j = job([
      { stage: Stage.INGEST, state: TaskState.SUCCEEDED },
      { stage: Stage.PREPROCESS, state: TaskState.QUEUED },
    ]);
    const done = j.tasks[0];
    if (done) done.finishedAt = timestampFromMs(Date.UTC(2026, 10, 14, 19, 50));
    const views = stageViews(j, Stage.PREPROCESS);
    expect(views[1]).toMatchObject({ state: "running", since: Date.UTC(2026, 10, 14, 19, 50) });
    expect(views[0]?.since).toBeUndefined();
  });

  test("a running task's own start, percent and activity", () => {
    const j = job([{ stage: Stage.INGEST, state: TaskState.RUNNING }]);
    j.createdAt = timestampFromMs(Date.UTC(2026, 10, 14, 19, 43, 30));
    const t = j.tasks[0];
    if (t) {
      t.startedAt = timestampFromMs(Date.UTC(2026, 10, 14, 19, 43, 31));
      t.percent = 35;
      t.activity = "NASA is cutting the files";
    }
    expect(stageViews(j, Stage.INGEST)[0]).toMatchObject({
      state: "running",
      since: Date.UTC(2026, 10, 14, 19, 43, 31),
      percent: 35,
      activity: "NASA is cutting the files",
    });
  });

  test("percent across several tasks counts finished ones as done", () => {
    const j = job([
      { stage: Stage.INGEST, state: TaskState.SUCCEEDED },
      { stage: Stage.INGEST, state: TaskState.RUNNING },
    ]);
    const t = j.tasks[1];
    if (t) t.percent = 50;
    expect(stageViews(j, Stage.INGEST)[0]?.percent).toBe(75);
  });

  test("a cancelled task shows as stopped", () => {
    const views = stageViews(
      job([
        { stage: Stage.INGEST, state: TaskState.SUCCEEDED },
        { stage: Stage.PREPROCESS, state: TaskState.CANCELED },
      ]),
      Stage.PREPROCESS,
    );
    expect(views.map((v) => v.state)).toEqual(["done", "stopped"]);
  });

  test("some tasks done and some queued is running", () => {
    const views = stageViews(
      job([
        { stage: Stage.PREPROCESS, state: TaskState.SUCCEEDED },
        { stage: Stage.PREPROCESS, state: TaskState.QUEUED },
      ]),
      Stage.PREPROCESS,
    );
    expect(views[1]?.state).toBe("running");
  });
});

test("isFinished", () => {
  expect(isFinished(undefined)).toBe(false);
  expect(isFinished(create(JobSchema, { state: JobState.RUNNING }))).toBe(false);
  expect(isFinished(create(JobSchema, { state: JobState.CANCELED }))).toBe(true);
  expect(isFinished(create(JobSchema, { state: JobState.FAILED }))).toBe(true);
});
