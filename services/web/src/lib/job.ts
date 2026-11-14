// Reading a Job from the gateway: where each stage is, and what went wrong.

import type { Job, TaskStatus } from "@gen/gateway/v1/gateway_pb";
import { JobState, TaskState } from "@gen/gateway/v1/gateway_pb";
import { Stage } from "@gen/gateway/v1/task_pb";
import { timestampMs } from "@bufbuild/protobuf/wkt";

export type StepState = "waiting" | "running" | "done" | "failed";

export interface StageView {
  stage: Stage;
  label: string;
  state: StepState;
  /** Set when every task of the stage came from the cache. */
  cached: boolean;
  /** How long the stage took, once it's done. */
  seconds?: number;
  /** When a running stage started, or could have, in ms since 1970. */
  since?: number;
  /** How much of a running stage is done, 0 to 100, when its worker says. */
  percent?: number;
  /** What a running stage is doing, for people, when its worker says. */
  activity?: string;
  /** The first failed task's message, for people. */
  error?: string;
  /** Whether running the job again later might work. */
  retryable?: boolean;
}

const stageLabels: Record<number, string> = {
  [Stage.INGEST]: "Fetch the data from NASA",
  [Stage.PREPROCESS]: "Clean it up and average it",
  [Stage.ANALYSIS]: "Test for a trend",
};

const order = [Stage.INGEST, Stage.PREPROCESS, Stage.ANALYSIS];

export function isFinished(job: Job | undefined): boolean {
  return job?.state === JobState.SUCCEEDED || job?.state === JobState.FAILED;
}

// Only ingest reports when a task starts, so a task the gateway lists as
// queued may well be running already, in preprocess for example. Anything
// handed out counts as running.
function stateOf(tasks: TaskStatus[]): StepState {
  if (tasks.length === 0) return "waiting";
  if (tasks.some((t) => t.state === TaskState.FAILED)) return "failed";
  if (tasks.every((t) => t.state === TaskState.SUCCEEDED)) return "done";
  return "running";
}

function firstStart(tasks: TaskStatus[]): number | undefined {
  const starts = tasks.map((t) => (t.startedAt ? timestampMs(t.startedAt) : NaN)).filter(Number.isFinite);
  return starts.length > 0 ? Math.min(...starts) : undefined;
}

/** The stage's share done: finished tasks count as 100, unreported ones as 0. */
function percentDone(tasks: TaskStatus[]): number | undefined {
  if (!tasks.some((t) => t.percent !== undefined)) return undefined;
  const sum = tasks.reduce((n, t) => n + (t.state === TaskState.SUCCEEDED ? 100 : (t.percent ?? 0)), 0);
  return Math.round(sum / tasks.length);
}

function lastFinish(tasks: TaskStatus[]): number | undefined {
  const ends = tasks.map((t) => (t.finishedAt ? timestampMs(t.finishedAt) : NaN)).filter(Number.isFinite);
  return ends.length > 0 ? Math.max(...ends) : undefined;
}

function duration(tasks: TaskStatus[]): number | undefined {
  const starts = tasks.map((t) => (t.startedAt ? timestampMs(t.startedAt) : NaN)).filter(Number.isFinite);
  const ends = tasks.map((t) => (t.finishedAt ? timestampMs(t.finishedAt) : NaN)).filter(Number.isFinite);
  if (starts.length === 0 || ends.length !== tasks.length) return undefined;
  return Math.max(0, (Math.max(...ends) - Math.min(...starts)) / 1000);
}

/** One row per stage the job runs, up to and including stopAfter. */
export function stageViews(job: Job | undefined, stopAfter: Stage): StageView[] {
  const last = order.indexOf(stopAfter);
  const stages = last < 0 ? order : order.slice(0, last + 1);
  const tasksOf = (stage: Stage) => (job?.tasks ?? []).filter((t) => t.meta?.stage === stage);
  return stages.map((stage, i) => {
    const tasks = tasksOf(stage);
    const failed = tasks.find((t) => t.state === TaskState.FAILED);
    const state = stateOf(tasks);
    // A stage can start once the one before it has finished; the first, once
    // the job exists.
    const previous = stages[i - 1];
    const couldStart =
      previous === undefined ? (job?.createdAt ? timestampMs(job.createdAt) : undefined) : lastFinish(tasksOf(previous));
    const running = state === "running";
    return {
      stage,
      label: stageLabels[stage] ?? "Unknown stage",
      state,
      cached: tasks.length > 0 && tasks.every((t) => t.cacheHit),
      seconds: state === "done" ? duration(tasks) : undefined,
      since: running ? (firstStart(tasks) ?? couldStart) : undefined,
      percent: running ? percentDone(tasks) : undefined,
      activity: running ? tasks.find((t) => t.state !== TaskState.SUCCEEDED && t.activity)?.activity : undefined,
      error: failed?.error?.userMessage || (failed ? "This step failed." : undefined),
      retryable: failed?.error?.retryable,
    };
  });
}
