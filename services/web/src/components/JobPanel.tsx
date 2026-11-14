import type { Job } from "@gen/gateway/v1/gateway_pb";
import type { Stage } from "@gen/gateway/v1/task_pb";
import type { RawDataset } from "@gen/ingest/v1/ingest_pb";
import { describePoint } from "../lib/bbox";
import { compare, type Day, formatMonth } from "../lib/dates";
import { type StageView, stageViews } from "../lib/job";
import { formatLabel, sourceLabel, stepLabel } from "../lib/sources";

export type Phase = "uploading" | "creating" | "watching" | "done";

export interface Run {
  phase: Phase;
  job?: Job;
  error?: string;
}

interface Props {
  run: Run | undefined;
  stopAfter: Stage;
}

// Never colour alone: each state has its own glyph as well as its hue.
const glyphs: Record<StageView["state"], string> = {
  waiting: "○",
  queued: "○",
  running: "◐",
  done: "●",
  failed: "✕",
};

function status(v: StageView): string {
  switch (v.state) {
    case "waiting":
      return "not started";
    case "queued":
      return "waiting for a worker";
    case "running":
      return "working";
    case "failed":
      return "failed";
    case "done": {
      const time = v.seconds === undefined ? "" : v.seconds < 1 ? ", <1 s" : `, ${Math.round(v.seconds)} s`;
      return `${v.cached ? "done, cached" : "done"}${time}`;
    }
  }
}

function size(bytes: bigint): string {
  const n = Number(bytes);
  if (n < 1024 * 1024) return `${Math.max(1, Math.round(n / 1024))} KB`;
  return `${(n / 1024 / 1024).toFixed(1)} MB`;
}

/** The dates the fetched files actually cover. */
function covered(ds: RawDataset): { start?: Day; end?: Day } {
  const starts = ds.files.map((f) => f.coverage?.start).filter((d): d is NonNullable<typeof d> => !!d);
  const ends = ds.files.map((f) => f.coverage?.end).filter((d): d is NonNullable<typeof d> => !!d);
  return { start: starts.sort(compare)[0] ?? ds.dates?.start, end: ends.sort(compare).at(-1) ?? ds.dates?.end };
}

function Row({ k, children }: { k: string; children: React.ReactNode }) {
  return (
    <div>
      <dt>{k}</dt>
      <dd>{children}</dd>
    </div>
  );
}

function Fetched({ ds }: { ds: RawDataset }) {
  const bytes = ds.files.reduce((sum, f) => sum + (f.blob?.sizeBytes ?? 0n), 0n);
  const { start, end } = covered(ds);
  return (
    <dl className="kv">
      <Row k="Came from">{sourceLabel(ds.source)}</Row>
      <Row k="Covers">
        {formatMonth(start)} to {formatMonth(end)}
        {ds.step ? `, ${stepLabel(ds.step)}` : ""}
      </Row>
      {ds.point && <Row k="Point">{describePoint(ds.point)}</Row>}
      <Row k="Files">
        {ds.files.length} × {formatLabel(ds.format)}, {size(bytes)}
      </Row>
      <Row k="Variable">{ds.variables.join(", ")}</Row>
    </dl>
  );
}

const titles: Record<Run["phase"], string> = {
  uploading: "Uploading your file",
  creating: "Starting the job",
  watching: "Job",
  done: "Job",
};

/** The job: each stage as it runs, what went wrong, and what came back. */
export function JobPanel({ run, stopAfter }: Props) {
  if (!run) return null;
  const views = stageViews(run.job, stopAfter);
  const raw = run.job?.results?.raw[0];

  return (
    <section className="job" aria-live="polite">
      <h2 className="step-title">{titles[run.phase]}</h2>
      {run.error && (
        <p className="alert" role="alert">
          <span className="alert-glyph" aria-hidden="true">
            ✕
          </span>
          <span>{run.error}</span>
        </p>
      )}
      {run.job && (
        <ol className="stages">
          {views.map((v) => (
            <li key={v.stage} className={`stage stage-${v.state}`}>
              <span className="stage-glyph" aria-hidden="true">
                {glyphs[v.state]}
              </span>
              <span>{v.label}</span>
              <span className="stage-status">{status(v)}</span>
              {v.error && (
                <p className="stage-error">
                  {v.error}
                  {v.retryable ? " Trying again later may work." : ""}
                </p>
              )}
            </li>
          ))}
        </ol>
      )}
      {raw && <Fetched ds={raw} />}
      {raw && run.phase === "done" && (
        <p className="note">Charts come next, once the preprocess service is running.</p>
      )}
    </section>
  );
}
