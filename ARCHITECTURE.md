# Trend Detective architecture

Draft from the design session on 2026-11-14, for NASA Space Apps 2026, "Be An Earth System Trend Detective!".
The hackathon runs Nov 14-15. Submissions are due Nov 15, 23:59 local time.

Still open:
- The demo cases.
- The final list of tests, which belongs to the analysts.

## What the user does

1. Zooms the map and selects a rectangle.
2. Picks a variable from the catalog. Each one comes with a plain-language definition.
3. Sees the data layer for that rectangle and scrubs through time with a slider.
4. Sees the region's time series with anomalies, a trend line and a confidence band.
5. Gets the stats verdict for the series: rate of change, significance, and years to significance.
6. Sees a trend heatmap of where inside the region things change. It has no time axis, because it summarizes the whole period.
7. Optionally asks the LLM for an overview or follow-up answers, using their own key.

## Services

| Service | Responsibility | Talks via | Language |
|---|---|---|---|
| gateway | Orchestrates jobs, owns job state, the catalog and the browser API, runs preflight checks | Connect to browser and narrator, NATS to stages, Redis | Go |
| ingest | Fetches raw bytes from sources through hexagonal adapters. Never parses scientific formats | NATS, SeaweedFS, Redis | Go |
| preprocess | Decodes formats into canonical `Series` and `Grid`, runs filter chains, aggregates regions, computes anomalies, writes the grid's month-by-month values for the slider | NATS, SeaweedFS | team's choice, Python expected |
| analysis | Runs the full test suite on series and fast per-cell tests on grids | NATS, SeaweedFS | team's choice, Python expected |
| narrator | Streams the LLM overview and Q&A from finished analysis results | Connect, SeaweedFS | team's choice |
| web | Map, time slider, charts, heatmap, chat | Connect to gateway, presigned URLs to SeaweedFS | TypeScript, React, Vite and Bun |

Rules:
- Stages never call each other. Each one reads its own queue and reports to the gateway. A job can stop after any stage, so preprocessing works on its own.
- The gateway is the only service that writes job and task state.
- Narrator isn't a job. It's a direct streaming call, and the gateway only makes it when the analysis is finished. Otherwise it returns `409`.

```
                  Connect (JSON over HTTP, server streaming)
  browser  <------------------------------------------>  gateway  ---- Connect stream ---->  narrator
     |                                                    |   |                               |
     | presigned GET / PUT                     Redis <----+   |  NATS JetStream               | reads results
     v                                     (job + task       |                               v
  SeaweedFS (S3)  <----------------------   state)           +--> tasks.ingest      --> ingest      --+
     ^                                                        +--> tasks.preprocess  --> preprocess  --+-- write outputs --> SeaweedFS
     |                                                        +--> tasks.analysis    --> analysis    --+
     +-------------------------------------------------------- events.stage.done <-- every stage
```

## Infrastructure

**NATS JetStream**

Bootstrap creates three streams. Each keeps messages for 7 days, the same as job state in Redis.

| Stream | Subjects | Retention | Read by |
|---|---|---|---|
| `TASKS` | `tasks.ingest`, `tasks.preprocess`, `tasks.analysis` | Work queue. A task is deleted once it's acked | One durable consumer per stage, filtered to its subject |
| `EVENTS` | `events.>`: `events.stage.done`, and `events.task.progress` while a task runs | Limits. Events stay after acks, so a job can be replayed | The gateway |
| `ADVISORIES` | `$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.TASKS.>` | Limits | The gateway |

- `TASKS` lists its subjects one by one, so a publish to a misspelled subject fails instead of sitting in the stream.
- Each service creates its own consumer at startup with `CreateOrUpdateConsumer`.
- Delivery is at least once, with a limit of 3 attempts and backoff. When a task hits the limit, NATS publishes an advisory. `ADVISORIES` stores it, so the gateway marks the task failed even if it was down when the advisory fired.
- Publishers set `Nats-Msg-Id` so duplicate publishes are dropped.
- Messages carry references and metadata only. Data lives in SeaweedFS.

**SeaweedFS (S3 API)**
- Buckets: `raw`, `prep`, `analysis` and `uploads`.
- The object key is a content hash of the stage's inputs, its settings and its code version, so "is it cached?" is a single HEAD request.
- Ingest writes `raw/<adapter>/<cache key>/files/<name>`, then `raw/<adapter>/<cache key>/dataset.json` with the `RawDataset`. The manifest goes last, so its presence means the dataset is complete.
- The browser downloads results and uploads CSVs through presigned URLs, so those files never pass through the gateway.

**Redis**
- Job and task state. The gateway is the only writer.
- AOF persistence is on.
- Jobs expire after 7 days. Losing job state never loses results, because results are keyed by content hash.
- Fan-in uses `HINCRBY` counters per stage, inside a Lua script that counts each task once, so a redelivered `StageDone` can't count twice. A stage is complete when its finished count equals its total. The key layout is in [services/gateway/README.md](services/gateway/README.md).
- Ingest keeps a counter of in-flight Harmony jobs per Earthdata token. It needs its own counter because a task waiting on `NakWithDelay` isn't reliably counted as in-flight by JetStream.

**Bootstrap**

A one-shot Go program in `deploy/bootstrap`. It runs on every `make up`, creates the streams and buckets that are missing, updates stream settings that changed, and exits. A service that uses NATS or S3 waits for it with `depends_on: bootstrap: condition: service_completed_successfully`.

**Observability**
- Go services use [the-watchers](https://github.com/lein3000zzz/the-watchers): slog logging, Prometheus metrics, OpenTelemetry traces and Pyroscope profiling.
- `traceparent` travels in NATS headers through a NATS carrier that keeps keys lowercase. OTel's `HeaderCarrier` capitalizes it to `Traceparent`, which Python's extractor won't find.
- Every log record carries `job_id` and `task_id`, added through `WithContextAttrs`.
- Python services use the same log field names and metric names.
- Jaeger, Prometheus, Grafana and Pyroscope run in an opt-in compose profile, `obs`, started with `make up-obs`. Config is in `deploy/obs`.
  - Traces: `make up-obs` sets `OTEL_EXPORTER_OTLP_ENDPOINT=http://jaeger:4318`. When it is set, `internal/obs` installs the OTel SDK with an OTLP/HTTP batch exporter (the-watchers only uses the OTel API and leaves the SDK to the app). When it is empty, there is no exporter and spans stay no-ops.
  - The gateway and narrator wrap their mux in `obs.HTTP`: a server span per request, parented on an incoming `traceparent`, and `watchers_http_operations_total` / `watchers_http_operation_duration_seconds` labelled by mux pattern, which is one per Connect procedure. `/healthz` is skipped. The gateway's narrator client sends `traceparent`, and NATS headers carry it to ingest, so a job is one trace from the request through ingest.
  - Metrics: Prometheus scrapes `/metrics` on each Go service's `ADMIN_ADDR`.
  - Profiles: Pyroscope doesn't scrape, so Grafana Alloy pulls `/debug/pprof/` from the same port (CPU, memory, goroutines) and pushes to Pyroscope.
  - Grafana has the three as provisioned data sources and four dashboards in `deploy/obs/grafana/dashboards/`, linked by the `trend-detective` tag. `trend-detective.json` is the home dashboard; `go-resources.json`, `ingest-sources.json` and `http-streams.json` use only metrics the Go services already export (the `watchers_http_*` and `watchers_ingest_*` families and the Go and process collectors), with a `service` variable. Ingest counts are attempts (retries and pending polls included), not tasks, and the duration histograms top out at 120 s for ingest and 10 s for HTTP, so quantiles cap there; the mean panels use the exact sums. Jaeger is pinned to 2.20.0, the last release with the `/api` endpoints Grafana's Jaeger data source searches with; 2.21 and later serve only `/api/v3`.
  - Not covered: preprocess and analysis send no spans or metrics yet. They keep `traceparent` on the `StageDone` they publish and log its trace ID.
- The gateway exposes `/debug/jobs/{id}` with each task's stage, attempts and errors, plus the raw `StageDone` events.
- While a task runs, a stage may publish `TaskProgress` on `events.task.progress`: once when it starts, then a percent and a line for people, such as "NASA's Harmony service is cutting the files to your rectangle" or "Downloading the files from NASA, 12 of 60". The gateway marks the task running and keeps the newest percent, so the app can show it. Progress is best effort. The gateway never lets an update overwrite a finished task, and drops one it can't record after three deliveries. Ingest reports it so far; preprocess and analysis can add it the same way.

## Job lifecycle

1. **CreateJob.** The gateway fills in catalog defaults (source, filter chain, tests), so stages get everything explicitly and never read the catalog themselves. Preflight checks then run:
   - Harmony capabilities for the collection. This endpoint is public.
   - The CMR granule count compared with `MAX_GRANULES`.
   - Giovanni quota compared with `QUOTA_CUTOFF`, when a Giovanni service is used.
   - The task cap.

   Dates partly outside the variable's available range are clamped to it, and dates entirely outside are refused. The NASA checks fail open: if NASA can't answer within 5 seconds, the job goes ahead, and ingest's own checks apply.

   The gateway then writes the job and its tasks to Redis and publishes `tasks.ingest`.
2. **Ingest.** It checks the cache, then calls the adapter. For Harmony, it counts granules in CMR, submits the job, sends itself delayed rechecks with `NakWithDelay` instead of making a worker wait, downloads the output files into `raw`, and publishes `StageDone` with a `RawDataset` reference. Those rechecks are JetStream deliveries too, so ingest counts failed attempts in Redis, and the consumer's delivery limit is only a high ceiling.
3. The gateway publishes `tasks.preprocess` with the filter chain.
4. **Preprocess.** It decodes the files, runs the chain, aggregates regions and writes `Series` and `Grid` documents to `prep`, with the grid's values and anomalies as raw float32 files beside them. Then it publishes `StageDone`.
5. The gateway publishes `tasks.analysis` with the test list.
6. **Analysis.** It writes results to `analysis` and publishes `StageDone`.
7. The gateway updates Redis at every step and streams progress to the browser through `WatchJob`.

Failures use `TaskError{code, retryable, user_message}`. Messages like "quota exceeded" or "no data in this rectangle" fail immediately with a readable reason. Errors like a Harmony 5xx are retried.

## Contracts

- Protobuf is the single source of truth. Messages travel over NATS as `protojson`, and HTTP APIs use Connect.
- All protos live in `proto/`, one package per owning service, for example `trenddetective.ingest.v1`.
- `buf generate` writes Go, Python and TypeScript code into `gen/`. The generated code is committed, so building a service doesn't need buf.
- Codegen uses remote plugins from the Buf Schema Registry, so nobody installs protoc plugins. We can also push the module to our BSR org for browsable schema docs.
- CI runs `buf lint`, a format check and `buf breaking` against `main`. It also fails if `gen/` is out of date.

| Owner | Messages |
|---|---|
| gateway | The shared types in `common.v1`: `BBox`, `DateRange`, `BlobRef` and a few more. The `Task` envelope with `TaskMeta{job_id, task_id, stage}`, plus `StageDone`, `TaskProgress`, `TaskError` and the public Connect API |
| ingest | `IngestTask` with one settings message per adapter, `RawDataset` (file refs, format, source, variable, rectangle, dates) |
| preprocess | `PreprocessTask`, filter-chain ops, `PreprocessResult`, and the `Series`, `Grid` and `CubeHeader` documents |
| analysis | `AnalysisTask`, `TestPlan` and the test specs, `AnalysisResult`, and the `SeriesAnalysis` and `GridAnalysis` documents, including fitted model parameters |
| narrator | `NarratorService.Narrate` |

The gateway publishes a `Task` envelope, and its `spec` is the stage's own task message. Stage packages therefore never import the gateway's, and the Go packages have no import cycles. Each stage answers with a `StageDone` that carries its result or a `TaskError`, plus the JetStream delivery count as `attempt`.

Result messages hold only `BlobRef`s. The documents themselves are protojson files in SeaweedFS. Grid values and cube data are raw little-endian binary files stored next to them.

`Series` holds times, values, anomalies, units, variable, rectangle, source, coverage fraction, and the reason each missing value is missing.

### Gateway API, first version

- `ListVariables`
- `CreateJob`: variable, rectangle, dates, source, stages to run, filter chain, test list
- `GetJob`, plus `WatchJob` as a stream
- `CancelJob`: stops a job, and its Harmony job at NASA
- `GetResultURL`: presigned JSON or CSV, plus the binary data for cubes and the grid's month-by-month values
- `CreateUploadURL`: presigned PUT for CSV uploads
- `Narrate`: a stream that takes the job, the question and the chat history
- `GetQuota`: the Giovanni quota used, and Harmony jobs running against the limit

## Data sources

Each source is an ingest adapter, chosen per request. Each catalog entry sets its own default with `default_source`.

| Adapter | What it returns | Notes |
|---|---|---|
| `harmony-subset` | NetCDF grids for a rectangle, dates and variables | HOSS service. Verified for GPM_3IMERGM, M2TMNXSLV, GLDAS_NOAH025_M, OMNO2d and GRACEDADM. Returns one file per granule because there's no concatenation. Also accepts polygons |
| `giovanni-area` | Area-averaged CSV | Harmony plus Giovanni averaging, with Giovanni's own variable IDs. Uses the monthly Giovanni quota. Serves OMI NO2 and groundwater in mm |
| `giovanni-point` | Point CSV | `api.giovanni.earthdata.nasa.gov/timeseries`, with Giovanni's own variable IDs. Answers in seconds, for the grid cell the point falls in. Uses the monthly Giovanni quota. A second source for the variables Giovanni has |
| `power` | Point JSON | NASA POWER, no login. The gateway sets the point to the middle of the rectangle |
| `csv-upload` | User CSV | The user's own data, or exports from Giovanni, Hydrology Time Series and AppEEARS. Ingest checks the file, stores a cleaned-up `date,value` copy and finds the time step. Catalog entry `user-csv` |
| `bundle` | Files shipped with the app | Offline demos and the video recording |
| `appeears` | Area-average CSV | NASA AppEEARS: 1 km MODIS plant greenness (NDVI, monthly) and daytime ground temperature (8-day), averaged over the rectangle. Logs in with an Earthdata username and password. Queued, so a job takes minutes |

- Earthdata login uses the team's token from an env var by default. Users can paste their own token to use their own quota.
- Each catalog entry carries `grid: true/false`. Series-only sources hide the slider and heatmap.
- Each entry also has a plain definition, display units, source and variable ID, dates, valid range, domain, default filter chain, default tests, and hints for the LLM.

## Regions and grids

The region is the unit of analysis everywhere. Pixels only feed aggregation.

1. The user selects a rectangle. `harmony-subset` fetches every pixel inside it in one request.
2. Preprocess builds the region-average series with latitude weighting and per-pixel anomalies averaged afterward, and tracks the valid-pixel fraction for each month.
3. Preprocess also builds a `Grid` of cells for per-cell analysis, the slider and the heatmap. Each cell is a square block of `k x k` native pixels, built like the region series: latitude-weighted, per-pixel anomalies, and its own coverage rule. Partial blocks along the bottom and right edges keep their real pixels. Cells are never a fixed `N x N` split of the rectangle, which would stretch them on a non-square one.
   - `k` is the job's `block_pixels`, else the variable's catalog default (1 for MERRA-2, 2 for GLDAS, 4 for IMERG), else `ceil(longer side / SUBREGION_N)`, else 1 for native pixels.
   - Preprocess then lowers `k` to leave at least 3 cells on the shorter side, and raises it to keep both sides within `MAX_GRID`; `MAX_GRID` wins.
   - The `Grid` records the `k` it used, how many pixels in each cell ever have data, and the cell edges.
   - Why blocks and not pixels or a fixed split: a synthetic test found the best cell size follows the size of the feature, not the rectangle, and square cells beat stretched ones of the same area for compact features. See Decisions › Region model.
4. The slider and the heatmap both show those cells. A different block size reruns only preprocess and analysis, because ingest's cache doesn't depend on it.

Size guards, all configurable:
- `MAX_GRANULES`, checked with CMR before submitting.
- `MAX_GRID`. Larger grids get bigger blocks, and the UI shows the resulting cell size.
- Daily products become monthly values in preprocess, using a minimum-valid-days rule.

**The slider's data.** The browser downloads the grid's values and anomalies once, as the float32 files `GetResultURL(ARTIFACT_GRID)` signs, and draws any month locally. A compact slider cube (16-bit values with a scale, offset and missing-data mask, and a JSON header; PRE-4) would halve that download; the contract has it, but nothing builds it yet.

## Preprocessing

The filter chain is an ordered list of named operations, kept in a registry inside preprocess. Examples:
- `qc_range`
- `monthly_mean{min_valid_days}`
- `anomaly{kind: absolute|percent|standardized, baseline}`
- `log10`
- `coarsen`, deprecated for grids: it averages before the region series, unweighted
- `gap_fill_display{max_gap}`

Rules:
- Every missing value records why it's missing.
- Gaps are never filled for statistics.
- The normal value for a calendar month needs data in a minimum share of baseline years.
- A month needs a minimum valid-pixel coverage to count.
- Calendar months whose standard deviation is zero are skipped for standardized anomalies.
- Percent change for °C is never shown.

## Analysis

The analysts can change this section.

- **Full suite on each region series:**
  - Regression slope with a 95% confidence interval, corrected for autocorrelation.
  - Mann-Kendall (Hamed-Rao version) or Seasonal Kendall, with Sen's slope.
  - A check that regression and Mann-Kendall agree.
  - Pettitt change-point test, residual checks, and outlier months by robust z-score.
  - Shuffle test, trend triangle, trend for each calendar month, and years to significance.
- **Fast tests on each grid cell:** regression with autocorrelation correction and Mann-Kendall with Sen's slope. The FDR correction runs across all cells, and the output is grids of slope, p-value, significance and valid-month count.
- **Results include the fitted model:** intercept, slope, standard errors, residual spread and lag-1 autocorrelation. These exist for later prediction bands.

## Narrator

- Speaks OpenAI Chat Completions, which also covers Ollama, OpenRouter, vLLM and LM Studio, plus Anthropic Messages.
- The user's key travels in a request header. It's held in memory only and never logged.
- Narrator stores nothing between calls. The browser sends the chat history each time.
- The LLM gets the analysis results as structured data and never computes numbers itself.
- After streaming, a final message lists any number in the answer that doesn't appear in the results.
- The app works fully without a key.

## Configuration

Settings are env vars per service, set in `compose.yaml` and read from `.env`. The catalog is a separate file, `services/gateway/catalog.yaml`.

| Variable | Default |
|---|---|
| `TASK_CAP` | 256 |
| `HARMONY_CONCURRENCY` | 4. Harmony jobs in flight per Earthdata token |
| `HARMONY_MAX_WAIT` | `40m`. A Harmony job NASA hasn't finished by then is cancelled and the task fails |
| `QUOTA_CUTOFF` | 0.9 |
| `MAX_GRANULES` | 1000 |
| `MAX_GRID` | 128 |
| `SUBREGION_N` | 8 |
| `EARTHDATA_TOKEN` | none, required |
| `NATS_URL` | `nats://nats:4222`, set in compose |
| `REDIS_URL` | `redis://redis:6379/0`, set in compose |
| `S3_ENDPOINT` | `http://seaweedfs:8333`, set in compose |
| `S3_ACCESS_KEY` | from `.env`. `.env.example` has a local value |
| `S3_SECRET_KEY` | from `.env`. `.env.example` has a local value |
| `S3_PUBLIC_ENDPOINT` | `http://localhost:8333`. The gateway signs presigned URLs for this host, so it must be what the browser uses |
| `S3_ALLOWED_ORIGINS` | `*`. Browser origins SeaweedFS accepts for presigned URLs |
| `PRESIGN_TTL` | `15m`. How long presigned URLs work |
| `MAX_UPLOAD_MB` | 20. Largest CSV upload. The gateway and ingest both read it |
| `LOG_LEVEL` | `info`. Go services: trace, debug, info, warn or error |
| `ADMIN_ADDR` | `:9090`. Go services serve `/metrics` and `/debug/pprof/` here, inside the compose network only |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | empty, so no spans leave the process. `make up-obs` sets `http://jaeger:4318`. Go services export spans over OTLP/HTTP when it is set. Compose passes only this one through; a binary run outside compose also reads the other `OTEL_EXPORTER_OTLP_*` HTTP exporter variables (headers, TLS, timeout, `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT`) |
| `BUNDLE_DIR` | `/bundle`. Ingest's bundled datasets, mounted from `services/ingest/bundle` |
| `INGEST_CONCURRENCY` | 16. Most ingest tasks wait on NASA, so this is well above `HARMONY_CONCURRENCY` |
| `INGEST_ACK_WAIT` | `2m`. Ingest sends a heartbeat every third of this while a task runs |
| `PREPROCESS_CONCURRENCY` | 2 |
| `PREPROCESS_ACK_WAIT` | `2m`. Preprocess sends a heartbeat every third of this while a task runs |
| `HTTP_ADDR` | `:8080`. The gateway's Connect API |
| `HARMONY_URL`, `CMR_URL` | `https://harmony.earthdata.nasa.gov`, `https://cmr.earthdata.nasa.gov` |
| `POWER_URL` | `https://power.larc.nasa.gov` |
| `GIOVANNI_URL` | `https://api.giovanni.earthdata.nasa.gov`. For the gateway's quota check and ingest's `giovanni-point` |
| `CORS_ORIGINS` | `*`. Comma-separated origins allowed to call the gateway from a browser |

## Repository

One public monorepo, [TheBaronessHotel/trend-detective](https://github.com/TheBaronessHotel/trend-detective). On 2026-11-14 the team switched to it from the first plan of one repo per service.

```
.
├── compose.yaml, .env.example, Makefile
├── buf.yaml, buf.gen.yaml
├── proto/trenddetective/<service>/v1/   contracts, one package per owning service
├── gen/                                 generated Go, Python and TS code, committed
├── deploy/                              NATS, Redis and SeaweedFS configs, bootstrap container
└── services/
    ├── gateway/      Go
    ├── ingest/       Go
    ├── preprocess/
    ├── analysis/
    ├── narrator/
    └── web/          TypeScript, React, Vite, Bun
```

| Target | What it does |
|---|---|
| `make up` | Creates `.env` on the first run, starts everything, and waits for bootstrap to finish |
| `make up-obs` | `make up` plus the `obs` profile: Jaeger, Prometheus, Grafana, and Pyroscope with Alloy. Points the Go services' spans at Jaeger |
| `make down` | Stops everything, the `obs` profile too. Data stays in the Docker volumes |
| `make logs` | Follows logs |
| `make ps` | Lists containers, including bootstrap after it exits |
| `make clean` | Stops everything and deletes all data |
| `make test` | Runs the Go unit tests |
| `make test-preprocess` | Runs the preprocess checks CI runs: lockfile, lint and unit tests. Needs uv |
| `make smoke` | Runs end-to-end checks against the running stack |
| `make smoke-harmony` | Runs a real GPM job through Harmony. Needs `EARTHDATA_TOKEN`, so CI skips it |
| `make smoke-power` | Runs a real job through NASA POWER. No token, but CI skips it |
| `make smoke-giovanni` | Runs a real point job through Giovanni's time-series API. Needs `EARTHDATA_TOKEN`, so CI skips it |
| `make smoke-appeears` | Runs a real NDVI job through AppEEARS. Needs `EARTHDATA_USERNAME` and `EARTHDATA_PASSWORD`, so CI skips it |
| `make smoke-airs` | Runs every AIRS map variable through all stages, methane's column average included. Needs `EARTHDATA_TOKEN`, so CI skips it |
| `make warm-examples` | Runs the web app's examples (`services/web/src/lib/examples.json`) once, so NASA's data is cached before a demo |
| `make web-dev` | The web app with hot reload on port 5173, against the running stack's gateway |
| `make web-check` | The web app's type check, unit tests and production build. CI runs it |
| `make gen` | Regenerates `gen/` from `proto/` |
| `make lint` | Lints and format-checks the protos |

Each service has its own Dockerfile in its folder. Compose builds with the repo root as the context, so every service can use `gen/`.

## Later, not a priority

- Dynamic regression and prediction. The model parameters are already in the results.
- Polygon regions. HOSS already accepts shapes.
- Images on GHCR.
- Slider cubes (PRE-4), to halve the month-by-month map's download.
- Groundwater and other daily-only products as maps. Harmony can't cut a monthly grid for them.
- Spans and metrics from the Python services, preprocess and analysis.
- Shareable links to a result, once there's a hosted demo.
