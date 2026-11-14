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
| preprocess | Decodes formats into canonical `Series` and `Grid`, runs filter chains, aggregates regions, computes anomalies, builds slider cubes | NATS, SeaweedFS | team's choice, Python expected |
| analysis | Runs the full test suite on series and fast per-cell tests on grids | NATS, SeaweedFS | team's choice, Python expected |
| narrator | Streams the LLM overview and Q&A from finished analysis results | Connect, SeaweedFS | team's choice |
| web | Map, time slider, charts, heatmap, chat | Connect to gateway, presigned URLs to SeaweedFS | team's choice |

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
| `EVENTS` | `events.>`, so far only `events.stage.done` | Limits. Events stay after acks, so a job can be replayed | The gateway |
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
- Jaeger, Prometheus, Grafana and Pyroscope run in an opt-in compose profile started with `make up-obs`.
- The gateway exposes `/debug/jobs/{id}` with each task's stage, attempts and errors.

## Job lifecycle

1. **CreateJob.** The gateway fills in catalog defaults (source, filter chain, tests), so stages get everything explicitly and never read the catalog themselves. Preflight checks then run:
   - Harmony capabilities for the collection. This endpoint is public.
   - The CMR granule count compared with `MAX_GRANULES`.
   - Giovanni quota compared with `QUOTA_CUTOFF`, when a Giovanni service is used.
   - The task cap.

   The gateway then writes the job and its tasks to Redis and publishes `tasks.ingest`.
2. **Ingest.** It checks the cache, then calls the adapter. For Harmony, it submits the job, sends itself delayed rechecks with `NakWithDelay` instead of making a worker wait, downloads the output files into `raw`, and publishes `StageDone` with a `RawDataset` reference.
3. The gateway publishes `tasks.preprocess` with the filter chain.
4. **Preprocess.** It decodes the files, runs the chain, aggregates regions and writes `Series` and `Grid` documents plus the slider cubes to `prep`. Then it publishes `StageDone`.
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
| gateway | The shared types in `common.v1`: `BBox`, `DateRange`, `BlobRef` and a few more. The `Task` envelope with `TaskMeta{job_id, task_id, stage}`, plus `StageDone`, `TaskError` and the public Connect API |
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
- `GetResultURL`: presigned JSON or CSV
- `CreateUploadURL`: presigned PUT for CSV uploads
- `Narrate`: a stream that takes the job, the question and the chat history
- `GetQuota`

## Data sources

Each source is an ingest adapter, chosen per request. Each catalog entry sets its own default with `default_source`.

| Adapter | What it returns | Notes |
|---|---|---|
| `harmony-subset` | NetCDF grids for a rectangle, dates and variables | HOSS service. Verified for GPM_3IMERGM, M2TMNXSLV, GLDAS_NOAH025_M, OMNO2d and GRACEDADM. Returns one file per granule because there's no concatenation. Also accepts polygons |
| `giovanni-area` | Area-averaged CSV | Harmony plus Giovanni averaging. Uses the monthly Giovanni quota. Not available for MERRA-2 monthly or OMNO2d |
| `giovanni-point` | Point CSV | `api.giovanni.earthdata.nasa.gov/timeseries` |
| `power` | Point JSON | NASA POWER, no login |
| `csv-upload` | User CSV | Exports from Giovanni, Hydrology Time Series and AppEEARS |
| `bundle` | Files shipped with the app | Offline demos and the video recording |
| `appeears` | MODIS NDVI and land surface temperature | Later |

- Earthdata login uses the team's token from an env var by default. Users can paste their own token to use their own quota.
- Each catalog entry carries `grid: true/false`. Series-only sources hide the slider and heatmap.
- Each entry also has a plain definition, display units, source and variable ID, dates, valid range, domain, default filter chain, default tests, and hints for the LLM.

## Regions and grids

The region is the unit of analysis everywhere. Pixels only feed aggregation.

1. The user selects a rectangle. `harmony-subset` fetches every pixel inside it in one request.
2. Preprocess builds the region-average series with latitude weighting and per-pixel anomalies averaged afterward, and tracks the valid-pixel fraction for each month.
3. Preprocess also aggregates pixels into a configurable `N x N` grid of sub-regions. The default `N` is 8, and setting `N` to the native resolution gives plain pixels.
4. The slider and the heatmap both show those sub-regions.

Size guards, all configurable:
- `MAX_GRANULES`, checked with CMR before submitting.
- `MAX_GRID`. Larger grids are block-averaged down, and the UI shows the resulting resolution.
- Daily products become monthly values in preprocess, using a minimum-valid-days rule.

**Slider cube.** One compact binary file per cube: 16-bit values with a scale, offset and missing-data mask. A JSON header gives the axes, units and color range. There's one cube for raw values and one for anomalies. The browser downloads it once and draws any month locally.

## Preprocessing

The filter chain is an ordered list of named operations, kept in a registry inside preprocess. Examples:
- `qc_range`
- `monthly_mean{min_valid_days}`
- `anomaly{kind: absolute|percent|standardized, baseline}`
- `log10`
- `coarsen`
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
| `HARMONY_CONCURRENCY` | 4 |
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
| `LOG_LEVEL` | `info`. Go services: trace, debug, info, warn or error |
| `ADMIN_ADDR` | `:9090`. Go services serve `/metrics` and `/debug/pprof/` here, inside the compose network only |
| `BUNDLE_DIR` | `/bundle`. Ingest's bundled datasets, mounted from `services/ingest/bundle` |
| `INGEST_CONCURRENCY` | 4 |
| `INGEST_ACK_WAIT` | `2m`. Ingest sends a heartbeat every third of this while a task runs |
| `HTTP_ADDR` | `:8080`. The gateway's Connect API |
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
    └── web/
```

| Target | What it does |
|---|---|
| `make up` | Creates `.env` on the first run, starts everything, and waits for bootstrap to finish |
| `make up-obs` | Starts everything plus the observability stack. Not built yet, comes with PLAT-5 |
| `make down` | Stops everything. Data stays in the Docker volumes |
| `make logs` | Follows logs |
| `make ps` | Lists containers, including bootstrap after it exits |
| `make clean` | Stops everything and deletes all data |
| `make test` | Runs the Go unit tests |
| `make smoke` | Runs end-to-end checks against the running stack |
| `make gen` | Regenerates `gen/` from `proto/` |
| `make lint` | Lints and format-checks the protos |

Each service has its own Dockerfile in its folder. Compose builds with the repo root as the context, so every service can use `gen/`.

## Later, not a priority

- Dynamic regression and prediction. The model parameters are already in the results.
- The AppEEARS adapter for MODIS NDVI and land surface temperature.
- Polygon regions. HOSS already accepts shapes.
- Images on GHCR.
- Demo cases for the video. Candidates:
  - OMI NO2 over Asia: east China falling, India rising.
  - GRACE groundwater in northwest India.
  - GPM rain in a noisy region, as the "not significant yet" case.
