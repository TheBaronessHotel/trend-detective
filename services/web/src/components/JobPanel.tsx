import { useEffect, useState } from "react";
import { type Job, JobState } from "@gen/gateway/v1/gateway_pb";
import { Stage } from "@gen/gateway/v1/task_pb";
import type { RawDataset } from "@gen/ingest/v1/ingest_pb";
import type { Grid, Series } from "@gen/preprocess/v1/data_pb";
import type { SeriesAnalysis } from "@gen/analysis/v1/results_pb";
import type { Heatmap } from "../lib/grid";
import { errorMessage } from "../api";
import { describePoint } from "../lib/bbox";
import { compare, type Day, formatMonth } from "../lib/dates";
import { type StageView, stageViews } from "../lib/job";
import { formatLabel, sourceLabel, stepLabel } from "../lib/sources";
import { CellChart } from "./CellChart";
import { SeriesChart } from "./SeriesChart";
import { HeatmapLegend } from "./HeatmapLegend";
import { NarratePanel } from "./NarratePanel";
import { VerdictPanel } from "./VerdictPanel";

export type Phase = "uploading" | "creating" | "watching" | "done";

export interface Run {
  phase: Phase;
  job?: Job;
  error?: string;
  /** The region's series, once preprocess has made it and it has downloaded. */
  series?: Series;
  /** Set while the series downloads. */
  loadingSeries?: boolean;
  seriesError?: string;
  /** The trend tests, when analysis ran and they downloaded. */
  analysis?: SeriesAnalysis;
  analysisError?: string;
  /** Per-cell trends, for gridded jobs, drawn on the globe. */
  heatmap?: Heatmap;
  /** The map grid behind the heatmap, for the month-by-month map. */
  grid?: Grid;
  /** Whether the job asked for the month-by-month map. */
  monthsMap?: boolean;
}

/** One map cell's own chart, opened by clicking the cell on the globe. */
export interface CellView {
  series: Series;
  /** The cell's trend test from the trend map, in words, when there is one. */
  trend: string;
}

interface Props {
  run: Run | undefined;
  /** A clicked map cell, shown under the region's chart. */
  cell?: CellView;
  onCloseCell?: () => void;
  stopAfter: Stage;
  /** The variable's name, for the chart's title. */
  seriesTitle: (variableId: string) => string;
  onSaveCsv: () => Promise<void>;
  /** Cancels the running job. */
  onStop: () => Promise<void>;
}

// Never colour alone: each state has its own glyph as well as its hue.
const glyphs: Record<StageView["state"], string> = {
  waiting: "○",
  running: "◐",
  done: "●",
  failed: "✕",
  stopped: "–",
};

/** The time now, ticking every second while active, so elapsed times count up. */
function useNow(active: boolean): number {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    if (!active) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [active]);
  return now;
}

function elapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return s < 60 ? `${s} s` : `${Math.floor(s / 60)} min ${s % 60} s`;
}

// What a long-running stage is doing, so a wait doesn't look like a hang.
const slowNotes: Partial<Record<Stage, string>> = {
  [Stage.INGEST]: "Fetching the data from NASA. Long date ranges and big rectangles can take several minutes.",
  [Stage.PREPROCESS]: "Reading and averaging the files. Many files take a while.",
};

/** What a running stage is doing: its worker's words, or a hint once it's slow. */
function StageNote({ v, now }: { v: StageView; now: number }) {
  const slow = v.since !== undefined && now - v.since > 30_000;
  const text = v.activity ?? (slow ? slowNotes[v.stage] : undefined);
  return text ? <p className="note stage-note">{text}</p> : null;
}

function status(v: StageView, now: number): string {
  switch (v.state) {
    case "waiting":
      return "not started";
    case "stopped":
      return "stopped";
    case "running": {
      const parts = ["working"];
      if (v.percent !== undefined) parts.push(`${v.percent}%`);
      if (v.since !== undefined) parts.push(elapsed(now - v.since));
      return parts.join(", ");
    }
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

function Alert({ children }: { children: React.ReactNode }) {
  return (
    <p className="alert" role="alert">
      <span className="alert-glyph" aria-hidden="true">
        ✕
      </span>
      <span>{children}</span>
    </p>
  );
}

/** The series chart, and the same numbers as a CSV file. */
function Results({
  jobId,
  series,
  analysis,
  heatmap,
  title,
  onSaveCsv,
  cell,
  onCloseCell,
}: {
  jobId: string;
  series: Series;
  analysis: SeriesAnalysis | undefined;
  heatmap: Heatmap | undefined;
  title: string;
  onSaveCsv: () => Promise<void>;
  cell?: CellView;
  onCloseCell?: () => void;
}) {
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState<string>();

  async function save() {
    setSaving(true);
    setSaveError(undefined);
    try {
      await onSaveCsv();
    } catch (err) {
      setSaveError(`Couldn't save the CSV. ${errorMessage(err)}`);
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="job-chart">
      <div className={heatmap ? "results-top with-heatmap" : "results-top"}>
        {analysis && <VerdictPanel analysis={analysis} />}
        {heatmap && <HeatmapLegend heatmap={heatmap} />}
      </div>
      <SeriesChart
        series={series}
        analysis={analysis}
        title={title}
        actions={
          <button type="button" className="action" onClick={save} disabled={saving}>
            {saving ? "Saving…" : "Save as CSV"}
          </button>
        }
      />
      {saveError && <Alert>{saveError}</Alert>}
      {cell && <CellChart cell={cell} title={title} onClose={onCloseCell} />}
      {analysis && <NarratePanel jobId={jobId} />}
    </div>
  );
}

/** The job: each stage as it runs, what went wrong, and what came back. */
export function JobPanel({ run, cell, onCloseCell, stopAfter, seriesTitle, onSaveCsv, onStop }: Props) {
  const [stopping, setStopping] = useState(false);
  // Folded down to one line, so the globe gets the room.
  const [folded, setFolded] = useState(false);
  // A clicked cell opens the panel to show its chart.
  useEffect(() => {
    if (cell) setFolded(false);
  }, [cell]);
  const views = stageViews(run?.job, stopAfter);
  const now = useNow(views.some((v) => v.state === "running"));
  if (!run) return null;
  const raw = run.job?.results?.raw[0];

  // With results, how the job ran folds away under them.
  if (run.series) {
    const done = views.filter((v) => v.state === "done").length;
    return (
      <section className={folded ? "job with-chart folded" : "job with-chart"}>
        <div className="job-head">
          <h2 className="step-title">Results</h2>
          <button type="button" className="action small" aria-expanded={!folded} onClick={() => setFolded((f) => !f)}>
            {folded ? "Show the results" : "Bigger map"}
          </button>
        </div>
        {!folded && (
          <>
            {run.error && <Alert>{run.error}</Alert>}
            {run.analysisError && <Alert>{run.analysisError}</Alert>}
            <Results
              jobId={run.job?.id ?? ""}
              series={run.series}
              analysis={run.analysis}
              heatmap={run.heatmap}
              title={seriesTitle(run.series.variableId)}
              onSaveCsv={onSaveCsv}
              cell={cell}
              onCloseCell={onCloseCell}
            />
            <details className="job-details">
              <summary>
                How it ran: {done} of {views.length} steps done
              </summary>
              <ol className="stages">
                {views.map((v) => (
                  <li key={v.stage} className={`stage stage-${v.state}`}>
                    <span className="stage-glyph" aria-hidden="true">
                      {glyphs[v.state]}
                    </span>
                    <span>{v.label}</span>
                    <span className="stage-status">{status(v, now)}</span>
                  </li>
                ))}
              </ol>
              {raw && <Fetched ds={raw} />}
            </details>
          </>
        )}
      </section>
    );
  }

  return (
    <section className="job">
      <div className="job-status" aria-live="polite">
        <div className="job-head">
          <h2 className="step-title">{run.job?.state === JobState.CANCELED ? "Job stopped" : titles[run.phase]}</h2>
          {run.phase === "watching" && run.job && (
            <button
              type="button"
              className="action small"
              disabled={stopping}
              onClick={() => {
                setStopping(true);
                void onStop().finally(() => setStopping(false));
              }}
            >
              {stopping ? "Stopping…" : "Stop"}
            </button>
          )}
        </div>
        {run.error && <Alert>{run.error}</Alert>}
        {run.job && (
          <ol className="stages">
            {views.map((v) => (
              <li key={v.stage} className={`stage stage-${v.state}`}>
                <span className="stage-glyph" aria-hidden="true">
                  {glyphs[v.state]}
                </span>
                <span>{v.label}</span>
                <span className="stage-status">{status(v, now)}</span>
                {v.state === "running" && <StageNote v={v} now={now} />}
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
        {run.loadingSeries && <p className="note">Loading the chart…</p>}
        {run.seriesError && <Alert>{run.seriesError}</Alert>}
        {run.analysisError && <Alert>{run.analysisError}</Alert>}
      </div>
    </section>
  );
}
