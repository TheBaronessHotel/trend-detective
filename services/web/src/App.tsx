import { lazy, Suspense, useEffect, useMemo, useRef, useState } from "react";
import type { Variable } from "@gen/gateway/v1/gateway_pb";
import { Stage } from "@gen/gateway/v1/task_pb";
import { Source } from "@gen/ingest/v1/ingest_pb";
import { errorMessage, gateway, uploadCsv } from "./api";
import { DatesPicker } from "./components/DatesPicker";
import { JobPanel, type Run } from "./components/JobPanel";
import { PlaceFields } from "./components/PlaceFields";
import { SourcePicker } from "./components/SourcePicker";
import { ThemeButton } from "./components/ThemeButton";
import { VariablePicker } from "./components/VariablePicker";
import { type Box, problem } from "./lib/bbox";
import { type Day, jobRange } from "./lib/dates";
import { isFinished } from "./lib/job";

// Jobs stop after fetching the data until the preprocess service runs in the
// stack. A later stage with no worker would leave the job waiting forever.
const STOP_AFTER = Stage.INGEST;

// MapLibre is most of the bundle, so the map loads on its own after the page.
const MapView = lazy(() => import("./components/MapView").then((m) => ({ default: m.MapView })));

export function App() {
  const [variables, setVariables] = useState<Variable[]>();
  const [loadError, setLoadError] = useState<string>();
  const [variableId, setVariableId] = useState<string>();
  const [box, setBox] = useState<Box>();
  const [start, setStart] = useState<Day>();
  const [end, setEnd] = useState<Day>();
  const [source, setSource] = useState<Source>();
  const [file, setFile] = useState<File>();
  const [run, setRun] = useState<Run>();
  const abort = useRef<AbortController | undefined>(undefined);

  useEffect(() => {
    gateway
      .listVariables({})
      .then((res) => setVariables(res.variables))
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
  }

  const blocker = !variable
    ? "Pick what to look at."
    : problem(box)
      ? problem(box)
      : !start || !end
        ? "Pick the dates."
        : source === Source.CSV_UPLOAD && !file
          ? "Choose your CSV file."
          : undefined;
  const busy = run !== undefined && run.phase !== "done";

  async function startJob() {
    if (blocker || !variable || !box || !start || !end) return;
    abort.current?.abort();
    const ctrl = new AbortController();
    abort.current = ctrl;
    const signal = ctrl.signal;
    try {
      let upload;
      if (source === Source.CSV_UPLOAD && file) {
        setRun({ phase: "uploading" });
        upload = await uploadCsv(file, signal);
      }
      setRun({ phase: "creating" });
      const dates = jobRange(start, end, variable.available);
      const { job } = await gateway.createJob(
        { spec: { variableId: variable.id, bbox: box, dates, source, upload, stopAfter: STOP_AFTER } },
        { signal },
      );
      if (!job) throw new Error("The gateway started no job.");
      setRun({ phase: "watching", job });
      let last = job;
      for await (const res of gateway.watchJob({ jobId: job.id }, { signal })) {
        if (res.job) {
          last = res.job;
          setRun({ phase: "watching", job: last });
        }
      }
      // The stream ends when the job finishes. If it broke off early, ask once.
      if (!isFinished(last)) last = (await gateway.getJob({ jobId: job.id }, { signal })).job ?? last;
      setRun({ phase: "done", job: last });
    } catch (err) {
      if (signal.aborted) return;
      setRun((r) => ({ ...r, phase: "done", error: errorMessage(err) }));
    }
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
        {variables && <VariablePicker variables={variables} selected={variable} onSelect={selectVariable} />}
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
        <button type="button" className="action primary run" disabled={!!blocker || busy} onClick={startJob}>
          {busy ? "Working…" : "Fetch the data"}
        </button>
        {blocker && <p className="note">{blocker}</p>}
      </aside>
      <main className="main">
        <Suspense fallback={<div className="map-wrap" />}>
          <MapView box={box} onBox={setBox} />
        </Suspense>
        <JobPanel run={run} stopAfter={STOP_AFTER} />
      </main>
    </div>
  );
}
