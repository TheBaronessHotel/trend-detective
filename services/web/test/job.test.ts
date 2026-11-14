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
  expect(isFinished(create(JobSchema, { state: JobState.FAILED }))).toBe(true);
});
