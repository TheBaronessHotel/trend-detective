import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import type { Topic, Variable } from "@gen/gateway/v1/gateway_pb";
import { Stage } from "@gen/gateway/v1/task_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";
import type { Job } from "@gen/gateway/v1/gateway_pb";
import {
  errorMessage,
  gateway,
  loadGrid,
  loadGridAnalysis,
  loadGridMonths,
  loadSeries,
  loadSeriesAnalysis,
  saveSeriesCsv,
  uploadCsv,
} from "./api";
import type { Example } from "./lib/examples";
import { heatmap, type Method } from "./lib/grid";
import { cellSeries, type MonthFrames, type MonthsKind } from "./lib/months";
import { formatSigned } from "./lib/series";
import { CellSizePicker } from "./components/CellSizePicker";
import { DatesPicker } from "./components/DatesPicker";
import { ExamplesBar } from "./components/ExamplesBar";
import { type CellView, JobPanel, type Run } from "./components/JobPanel";
import { MonthsBar } from "./components/MonthsBar";
import { ALL_OUTPUTS, makesMap, type Outputs, OutputsPicker } from "./components/OutputsPicker";
import { PlaceFields } from "./components/PlaceFields";
import { SourcePicker } from "./components/SourcePicker";
import { ThemeButton } from "./components/ThemeButton";
import { VariablePicker } from "./components/VariablePicker";
import { type Box, problem } from "./lib/bbox";
import { compare, type Day, jobRange } from "./lib/dates";
import { isFinished } from "./lib/job";

// Jobs run every stage: ingest, preprocess and the trend tests.
const STOP_AFTER = Stage.ANALYSIS;

// MapLibre is most of the bundle, so the map loads on its own after the page.
const MapView = lazy(() => import("./components/MapView").then((m) => ({ default: m.MapView })));

/**
 * Follows a job until it finishes. The stream ends by itself then; if it
 * drops first, say on a network blip, the job carries on at the gateway, so
 * this asks for it and watches again, waiting a little longer each time.
 */
async function watchUntilFinished(job: Job, signal: AbortSignal, onJob: (j: Job) => void): Promise<Job> {
  let last = job;
  for (let failures = 0; ; ) {
    try {
      for await (const res of gateway.watchJob({ jobId: job.id }, { signal })) {
        if (res.job) {
          last = res.job;
          failures = 0;
          onJob(last);
        }
      }
    } catch (err) {
      if (signal.aborted || ++failures > 8) throw err;
      await new Promise((r) => setTimeout(r, Math.min(1000 * 2 ** failures, 15_000)));
    }
    if (isFinished(last)) return last;
    // Ended or dropped early: read the job once, then watch again.
    last = (await gateway.getJob({ jobId: job.id }, { signal })).job ?? last;
    onJob(last);
    if (isFinished(last)) return last;
  }
}

export function App() {
  const [variables, setVariables] = useState<Variable[]>();
  const [topics, setTopics] = useState<Topic[]>([]);
  const [loadError, setLoadError] = useState<string>();
  const [variableId, setVariableId] = useState<string>();
  const [box, setBox] = useState<Box>();
  const [start, setStart] = useState<Day>();
  const [end, setEnd] = useState<Day>();
  const [source, setSource] = useState<Source>();
  const [file, setFile] = useState<File>();
  // Native pixels per heatmap cell side; undefined uses the variable's default.
  const [block, setBlock] = useState<number>();
  // What the job makes beyond the region's chart.
  const [outputs, setOutputs] = useState<Outputs>(ALL_OUTPUTS);
  const [run, setRun] = useState<Run>();
  const abort = useRef<AbortController | undefined>(undefined);

  // The month-by-month map, downloaded the first time it's opened.
  const [frames, setFrames] = useState<MonthFrames>();
  const [monthsOn, setMonthsOn] = useState(false);
  const [monthsLoading, setMonthsLoading] = useState(false);
  const [monthsError, setMonthsError] = useState<string>();
  const [kind, setKind] = useState<MonthsKind>("anomalies");
  const [t, setT] = useState(0);
  const months = useMemo(() => (monthsOn && frames ? { frames, kind, t } : undefined), [monthsOn, frames, kind, t]);

  function resetMonths() {
    setCell(undefined);
    setFrames(undefined);
    setMonthsOn(false);
    setMonthsLoading(false);
    setMonthsError(undefined);
    setT(0);
  }

  // A job with the month-by-month map but no trend map opens straight to it.
  useEffect(() => {
    if (run?.phase === "done" && run.grid && run.monthsMap && !run.heatmap && !frames && !monthsLoading && !monthsError) {
      void openMonths();
    }
    // openMonths reads the run it's given here.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [run]);

  /**
   * Downloads the month-by-month data the first time something needs it: the
   * time slider, or a clicked cell's chart. undefined if it couldn't.
   */
  async function ensureFrames(): Promise<MonthFrames | undefined> {
    if (frames) return frames;
    const job = run?.job;
    const grid = run?.grid;
    if (!job || !grid) return undefined;
    // A new job aborts this one's signal, so a late download is dropped.
    const signal = abort.current?.signal;
    setMonthsLoading(true);
    setMonthsError(undefined);
    try {
      const f = await loadGridMonths(job.id, grid, signal);
      if (signal?.aborted) return undefined;
      if (!f) throw new Error("This job has no month-by-month map.");
      setFrames(f);
      setKind(f.anomalies ? "anomalies" : "values");
      setT(0);
      return f;
    } catch (err) {
      if (!signal?.aborted) setMonthsError(`Couldn't load the months. ${errorMessage(err)}`);
      return undefined;
    } finally {
      if (!signal?.aborted) setMonthsLoading(false);
    }
  }

  async function openMonths() {
    if (await ensureFrames()) setMonthsOn(true);
  }

  // A clicked map cell, whose own chart shows under the region's.
  const [cell, setCell] = useState<number>();
  function pickCell(c: number) {
    setCell(c);
    void ensureFrames();
  }
  const cellView = useMemo((): CellView | undefined => {
    if (cell === undefined || !frames || !run?.series || !run.grid) return undefined;
    const series = cellSeries(run.series, run.grid, frames, cell);
    if (!series) return undefined;
    const tested = run.heatmap?.cells.features.find((f) => f.properties.cell === cell)?.properties;
    const units = run.heatmap?.units ? ` ${run.heatmap.units}` : "";
    const trend = tested
      ? `This cell's trend: ${formatSigned(Number(tested.slope.toPrecision(3)))}${units} per decade, ${tested.significant ? "which passes the test after correcting for testing many cells at once" : "which could be chance"}.`
      : run.heatmap
        ? "This cell had too little data for a trend test."
        : "The trend map wasn't made for this job, so this cell has no trend test.";
    return { series, trend };
  }, [cell, frames, run]);

  useEffect(() => {
    gateway
      .listVariables({})
      .then((res) => {
        setVariables(res.variables);
        setTopics(res.topics);
      })
      .catch((err) => setLoadError(errorMessage(err)));
    return () => abort.current?.abort();
  }, []);

  const variable = useMemo(() => variables?.find((v) => v.id === variableId), [variables, variableId]);

  function selectVariable(id: string) {
    const v = variables?.find((x) => x.id === id);
    setVariableId(id);
    setStart(v?.available?.start);
    setEnd(v?.available?.end);
    setSource(v?.defaultSource);
    setFile(undefined);
    setBlock(undefined);
  }

  const blocker = !variable
    ? "Pick what to look at."
    : problem(box)
      ? problem(box)
      : !start || !end
        ? "Pick the dates."
        : compare(start, end) > 0
          ? "The start has to come before the end."
          : source === Source.CSV_UPLOAD && !file
          ? "Choose your CSV file."
          : undefined;
  const busy = run !== undefined && run.phase !== "done";

  // Bumped to move the globe to the rectangle, as when an example fills it in.
  const [focus, setFocus] = useState(0);

  /** Fills in the steps with an example and runs it at once. */
  function pickExample(e: Example) {
    const v = variables?.find((x) => x.id === e.variableId);
    if (!v) return;
    setVariableId(v.id);
    setBox(e.box);
    setStart(e.start);
    setEnd(e.end);
    setSource(v.defaultSource);
    setFile(undefined);
    setBlock(undefined);
    setFocus((n) => n + 1);
    void startJob({
      variable: v,
      box: e.box,
      start: e.start,
      end: e.end,
      source: v.defaultSource,
      file: undefined,
      block: undefined,
      // Examples show everything.
      outputs: ALL_OUTPUTS,
    });
  }

  interface JobInput {
    variable: Variable;
    box: Box;
    start: Day;
    end: Day;
    source: Source | undefined;
    file: File | undefined;
    block: number | undefined;
    outputs: Outputs;
  }

  // An example passes its own values, since state set in the same click
  // isn't there yet.
  async function startJob(given?: JobInput) {
    if (!given && (blocker || !variable || !box || !start || !end)) return;
    const input = given ?? { variable: variable!, box: box!, start: start!, end: end!, source, file, block, outputs };
    const map = makesMap(input.variable, input.source);
    abort.current?.abort();
    resetMonths();
    const ctrl = new AbortController();
    abort.current = ctrl;
    const signal = ctrl.signal;
    try {
      let upload;
      if (input.source === Source.CSV_UPLOAD && input.file) {
        setRun({ phase: "uploading" });
        upload = await uploadCsv(input.file, signal);
      }
      setRun({ phase: "creating" });
      const dates = jobRange(input.start, input.end, input.variable.available);
      const { job } = await gateway.createJob(
        {
          spec: {
            variableId: input.variable.id,
            bbox: input.box,
            dates,
            source: input.source,
            upload,
            stopAfter: STOP_AFTER,
            // The gateway drops these tests, and with none left ends the job
            // at preprocess, which has the chart and the map already.
            skipSeriesTests: !input.outputs.regionTests,
            skipGridTests: !map || !input.outputs.trendMap,
            ...(input.block ? { aggregation: { blockPixels: input.block } } : {}),
          },
        },
        { signal },
      );
      if (!job) throw new Error("The gateway started no job.");
      setRun({ phase: "watching", job });
      const last = await watchUntilFinished(job, signal, (j) => setRun({ phase: "watching", job: j }));
      // The series shows even when the trend tests failed or weren't run,
      // so a job that got through preprocess always draws its chart.
      const hasSeries = !!last.results?.preprocess?.series;
      const hasAnalysis = !!last.results?.analysis?.series;
      const hasGrid = !!last.results?.preprocess?.grid;
      const hasHeatmap = !!last.results?.analysis?.grid && hasGrid;
      // The grid also feeds the month-by-month map, when that was asked for.
      const wantsGrid = hasHeatmap || (hasGrid && input.outputs.monthsMap);
      setRun({ phase: "done", job: last, loadingSeries: hasSeries });
      if (!hasSeries) return;
      const [series, analysis, grid, gridAnalysis] = await Promise.allSettled([
        loadSeries(job.id, signal),
        hasAnalysis ? loadSeriesAnalysis(job.id, signal) : Promise.resolve(undefined),
        wantsGrid ? loadGrid(job.id, signal) : Promise.resolve(undefined),
        hasHeatmap ? loadGridAnalysis(job.id, signal) : Promise.resolve(undefined),
      ]);
      if (signal.aborted) return;
      setRun({
        phase: "done",
        job: last,
        series: series.status === "fulfilled" ? series.value : undefined,
        seriesError: series.status === "rejected" ? `Couldn't load the chart. ${errorMessage(series.reason)}` : undefined,
        analysis: analysis.status === "fulfilled" ? analysis.value : undefined,
        analysisError:
          analysis.status === "rejected" ? `Couldn't load the trend tests. ${errorMessage(analysis.reason)}` : undefined,
        // The heatmap is extra: if it can't load, the rest still shows.
        heatmap:
          grid.status === "fulfilled" && gridAnalysis.status === "fulfilled" && grid.value && gridAnalysis.value
            ? heatmap(grid.value, gridAnalysis.value)
            : undefined,
        grid: grid.status === "fulfilled" ? grid.value : undefined,
        gridAnalysis: gridAnalysis.status === "fulfilled" ? gridAnalysis.value : undefined,
        monthsMap: input.outputs.monthsMap,
      });
    } catch (err) {
      if (signal.aborted) return;
      setRun((r) => ({ ...r, phase: "done", loadingSeries: false, error: errorMessage(err) }));
    }
  }

  // The map's colours from the other test: redraw the heatmap from the same results.
  function pickMethod(m: Method) {
    setRun((r) => (r?.grid && r.gridAnalysis ? { ...r, heatmap: heatmap(r.grid, r.gridAnalysis, m) ?? r.heatmap } : r));
  }

  const seriesTitle = (id: string) => variables?.find((v) => v.id === id)?.name ?? id;

  // The watch ends by itself once the gateway marks the job cancelled.
  async function stopJob() {
    const job = run?.job;
    if (!job) return;
    try {
      await gateway.cancelJob({ jobId: job.id });
    } catch (err) {
      setRun((r) => (r ? { ...r, error: `Couldn't stop the job. ${errorMessage(err)}` } : r));
    }
  }

  function saveCsv() {
    const job = run?.job;
    if (!job) return Promise.resolve();
    return saveSeriesCsv(job.id, `trend-detective-${run?.series?.variableId || "series"}.csv`);
  }

  return (
    <div className="app">
      <header className="site">
        <span className="brand">Trend Detective</span>
        <p className="tagline">Pick something to measure and a place, and see whether it's changing.</p>
        <ThemeButton />
      </header>
      <aside className="sidebar">
        {loadError && (
          <p className="alert" role="alert">
            <span className="alert-glyph" aria-hidden="true">
              ✕
            </span>
            <span>{loadError}</span>
          </p>
        )}
        {!variables && !loadError && <p className="note">Loading the variables…</p>}
        <ExamplesBar variables={variables} busy={busy} onPick={pickExample} />
        {variables && <VariablePicker variables={variables} topics={topics} selected={variable} onSelect={selectVariable} />}
        <PlaceFields box={box} onBox={setBox} />
        <DatesPicker
          variable={variable}
          start={start}
          end={end}
          onChange={(s, e) => {
            setStart(s);
            setEnd(e);
          }}
        />
        <SourcePicker variable={variable} source={source} onSource={setSource} file={file} onFile={setFile} />
        <CellSizePicker variable={variable} source={source} box={box} block={block} onBlock={setBlock} />
        <OutputsPicker variable={variable} source={source} outputs={outputs} onOutputs={setOutputs} />
        <button type="button" className="action primary run" disabled={!!blocker || busy} onClick={() => void startJob()}>
          {busy ? "Working…" : "Fetch the data"}
        </button>
        {blocker && <p className="note">{blocker}</p>}
      </aside>
      <main className="main">
        <Suspense fallback={<div className="map-wrap" />}>
          <MapView box={box} onBox={setBox} heatmap={run?.heatmap} focus={focus} months={months} onCell={pickCell}>
            {run?.grid && run.monthsMap && (
              <MonthsBar
                canClose={!!run.heatmap}
                frames={frames}
                loading={monthsLoading}
                error={monthsError}
                on={monthsOn}
                kind={kind}
                t={t}
                onOpen={() => void openMonths()}
                onClose={() => setMonthsOn(false)}
                onKind={setKind}
                onT={setT}
              />
            )}
          </MapView>
        </Suspense>
        {/* Keyed by job, so a new job starts with the panel open. */}
        <JobPanel
          key={run?.job?.id ?? "new"}
          run={run}
          cell={cellView}
          onCloseCell={() => setCell(undefined)}
          onMethod={pickMethod}
          // A job without tests ends at preprocess; its spec says where.
          stopAfter={run?.job?.spec?.stopAfter || STOP_AFTER}
          seriesTitle={seriesTitle} onSaveCsv={saveCsv} onStop={stopJob} />
      </main>
    </div>
  );
}
