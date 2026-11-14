# ingest

Go. Fetches raw files from NASA sources into the `raw` bucket. It reads `gateway.v1.Task` messages from `tasks.ingest` and reports a `StageDone` for each one on `events.stage.done`. Ingest moves bytes only and never parses NetCDF, which is preprocess's job.

## Layout

The service is hexagonal: the core knows nothing about NATS or S3, and each side plugs in through an interface.

| Path | What it is |
|---|---|
| `cmd/ingest` | Wiring: settings, the-watchers, S3, NATS |
| `internal/core` | The domain. Picks the adapter, checks the cache, writes files and the manifest, and maps errors to `TaskError` |
| `internal/worker` | The JetStream consumer that drives the core |
| `internal/bundle` | The `bundle` adapter |
| `internal/harmony` | The `harmony-subset` and `giovanni-area` adapters, which share Harmony's job flow |
| `internal/giovanni` | The `giovanni-point` adapter |
| `internal/csvupload` | The `csv-upload` adapter |
| `internal/earthdata` | Picks a task's Earthdata token: the user's own if the gateway stored one, else the team's |
| `internal/power` | The `power` adapter |
| `bundle/` | Datasets shipped with the app. See [bundle/README.md](bundle/README.md) |

To add a source, write a `core.Adapter` and pass it to `core.New` in `cmd/ingest`. An adapter:
- writes files through the `core.Sink` it gets, one `Put` per file;
- returns the format, variables and original source;
- returns a `Fingerprint` that changes whenever its output for the same task could change, like its own version.

## Where files go

Each task has a cache key: a SHA-256 of the `IngestTask` (minus the Earthdata token key) and the adapter's fingerprint. Files land in the raw bucket under that key:

```
raw/<adapter>/<cache key>/files/<file name>
raw/<adapter>/<cache key>/dataset.json     the RawDataset, written last
```

Before fetching, ingest looks for `dataset.json`. If it's there, the task reuses that dataset and reports `cache_hit: true`, which costs no NASA quota. The manifest is written last, so its presence means every file is in.

## harmony-subset

Asks NASA Harmony's HOSS service for the task's variables, rectangle and dates, and gets back one NetCDF-4 file per granule. A Harmony job takes minutes, so no worker waits on one. Each delivery of the task moves it one step and then NAKs with a delay:

1. **No job yet.**
   - Count the granules in CMR. Zero fails with `NO_DATA`, and more than the task's `max_granules` fails with `TOO_LARGE`.
   - Take one of the token's `HARMONY_CONCURRENCY` slots. If none is free, check again in 30 seconds.
   - Submit with `forceAsync=true&skipPreview=true`, save the job ID, and check again in 10 seconds.
2. **Running.** Check again in 15 seconds.
3. **Done.** Free the slot, then download every output file, 4 at a time. Each file carries the dates its granule covers.
4. **Failed.** Free the slot and report `UPSTREAM`.

The token is the user's own, if the gateway stored one for the job. Otherwise it's `EARTHDATA_TOKEN`. Harmony answers a missing or bad token by redirecting to the Earthdata login page. The client stops at that redirect and reports `UNAUTHENTICATED`.

GES DISC, which serves GPM and MERRA-2, only hands files to Earthdata accounts that have approved its "NASA GESDISC DATA ARCHIVE" app once, at https://urs.earthdata.nasa.gov/approve_app?client_id=e2WVk8Pw6weeLUKZYOxvTQ. Until then, Harmony's subsetter gets 403 on every granule, and the adapter reports `UNAUTHENTICATED` with that link.

Variable names are paths inside the file, like `Grid/precipitation` for GPM IMERG. They go in the `variable` query parameter, not the URL path.

Redis keys:

| Key | Holds |
|---|---|
| `td:ingest:harmony:job:<cache key>` | The Harmony job ID, for 24 hours. A restart, or a second task for the same data, picks up the same job |
| `td:ingest:harmony:slots:<token hash>` | A sorted set of the jobs running on one token. Slots older than 6 hours are freed |
| `td:ingest:failures:<task ID>` | Failed attempts, see below |

## giovanni-area

Asks Harmony's Giovanni averaging service for the average over the rectangle, one value per time step, as a single CSV. It's for daily products with too many files to fetch as maps, like OMI NO2 and groundwater.

- It shares everything with `harmony-subset`: the token, the per-token slots, polling with `NakWithDelay`, and the failure messages. The differences:
  - it adds `average=area` to the request;
  - it skips the CMR granule count, because Giovanni reads the granules itself;
  - it stores the result as `text/csv` with `FORMAT_GIOVANNI_CSV`.
- The task needs Giovanni's own variable ID, from `api.giovanni.earthdata.nasa.gov/configured-variables/`, plus the CMR collection ID, which Harmony needs in the URL. The averaging service refuses the variable's path inside the file.
- The CSV has a few header lines, a blank line, then `time,mean_<id>` with one row per day. Days without data have an empty value, and their timestamp can use a space instead of `T`.
- It uses the Giovanni quota. The gateway's preflight refuses new jobs once `QUOTA_CUTOFF` is reached.
- A year of daily NO2 takes about 4 minutes on NASA's side, and two years of groundwater well under a minute.

## giovanni-point

Asks Giovanni's time-series API, `/timeseries`, for one variable at one point and stores the answer as `point.csv`. The gateway sets the point to the middle of the rectangle.

- The API answers right away, in about 4 seconds even for twenty years of daily data (7,305 rows, about 220 KB). So there's no job to poll, no slot, and no Harmony.
- It takes the same Giovanni variable IDs as `giovanni-area`, but no collection ID.
- Giovanni answers for the grid cell the point falls in. The CSV header gives that cell's center, for example 30.625, 75.625 for a request at 30.5, 75.5, and the dataset records it as its point.
- The CSV has `key,value` header lines (`unit`, `undef`, `lat`, `lon` and more), a blank line, then `Timestamp (UTC),Data` with one row per time step. Missing steps hold the header's `undef` value, such as `-9999` or `-1.26765e+30`. It's stored with `FORMAT_GIOVANNI_POINT_CSV`, because its layout differs from the area CSV.
- When every step is missing, the task fails with `NO_DATA` and stores nothing. That's what Giovanni returns for a land variable over the sea, or for NOBM chlorophyll on land or near a coast.
- Giovanni wraps most errors in a 403 and puts the real code in a JSON body. `code` 401 is a missing or bad token (`UNAUTHENTICATED`), and 400 an unknown variable (`INVALID_REQUEST`). A 404 means no data for those dates (`NO_DATA`). A 429, or any error that mentions the quota, is `QUOTA_EXCEEDED`, and a 5xx is transient.
- A single pixel has more gaps than an area average. Twenty years of daily OMI NO2 at one point near Hefei had 62% of the days missing, mostly from clouds.
- It uses the Giovanni quota, which the gateway's preflight checks.

## csv-upload

Reads a CSV the user uploaded through the gateway's `CreateUploadURL`, checks it, and stores a cleaned-up copy as `user.csv` with `FORMAT_USER_CSV`. The catalog entry is `user-csv`.

- It only reads from the `uploads` bucket, and the gateway refuses upload refs that point anywhere else.
- A signed PUT can't limit the size, so the adapter checks `MAX_UPLOAD_MB` twice: once from the object's size, and again while reading, in case the file changed in between. A file over the limit fails with `TOO_LARGE`.
- The cache key includes the upload's ETag, because the signed URL can be used again to replace the file under the same key.
- What it accepts:
  - a date column and a value column, separated by commas, semicolons or tabs, with or without a header row. With more columns, it uses the one headed `value`;
  - dates like `2010-03-01`, `2010/3/1`, `2010-03` or `2010-03-01T12:00`. It refuses `03/04/2010`, because day-first and month-first can't be told apart, and whole years;
  - a decimal comma, like `12,5`, when the columns are separated by semicolons or tabs;
  - missing values written as empty, `NA`, `N/A`, `NaN`, `null`, `-`, `-999`, `-9999` or `-99999`;
  - an Excel byte order mark, and `#` comment lines;
  - a preamble, like the metadata lines at the top of a Giovanni export. Rows before the first one that starts with a date are skipped, the last of them is the header, and a fill value they declare (`undef`, `Fill Value:`) counts as missing. Giovanni's area and point exports both upload as they are.
- It sorts the rows, refuses repeated dates, and needs at least two years and at least 12 values within the job's dates.
- It finds the time step from the usual gap between dates, as daily, weekly or monthly, and records it in `RawDataset.step`, because the catalog can't know it. Monthly data is stored as the first of each month. Preprocess decides whether daily data still needs `monthly_mean`.
- The cleaned-up file has a `date,value` header, ISO dates and an empty value for missing ones. The value column's own header, like `rain (mm)`, goes into `RawDataset.variables`.
- Every problem with the file fails the task with `INVALID_REQUEST` and a message saying what to change, with the row number as a spreadsheet would show it, for example `Row 14: "abc" isn't a number.` A file with nothing in the job's dates fails with `NO_DATA`.

## power

Fetches NASA POWER monthly means for one point, with no login. The gateway sets the point to the middle of the rectangle.

- It makes one request to `/api/temporal/monthly/point` and stores the response as `power.json`, the same format as the Delhi bundle.
- The monthly endpoint takes whole years, so preprocess trims the file to the task's dates.
- Every complete year has a 13th "month" holding the annual mean, and missing values are `-999`.
- POWER explains a 422 in plain English, for example "The data is available to 2026/11/30.", so that text goes to the user as `INVALID_REQUEST`. A 429 or a 5xx is transient.

## Failures

- A transient error, like a network failure or a NASA 5xx, NAKs the message with a 10 second backoff, then 1 minute. After 3 failed attempts in total, the task is reported failed.
- Any other error publishes a `StageDone` with a `TaskError` and acks.
- Failed attempts are counted in Redis, not by JetStream. Every Harmony poll is a JetStream delivery too, so the consumer's delivery limit is only a ceiling, at 2000.
- While a task runs, the worker sends a heartbeat every third of `INGEST_ACK_WAIT`, so a long download isn't redelivered.

## Settings

| Variable | Default | |
|---|---|---|
| `NATS_URL`, `REDIS_URL`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | required | Set by compose |
| `EARTHDATA_TOKEN` | empty | The team's token, from `.env`. Without it, Harmony and Giovanni tasks fail unless the user gave their own |
| `HARMONY_CONCURRENCY` | 4 | Harmony jobs in flight per token |
| `HARMONY_URL` | `https://harmony.earthdata.nasa.gov` | |
| `CMR_URL` | `https://cmr.earthdata.nasa.gov` | |
| `POWER_URL` | `https://power.larc.nasa.gov` | |
| `GIOVANNI_URL` | `https://api.giovanni.earthdata.nasa.gov` | For `giovanni-point` |
| `MAX_UPLOAD_MB` | 20 | Largest CSV upload. Keep it the same as the gateway's |
| `BUNDLE_DIR` | `/bundle` | Compose mounts `services/ingest/bundle` here |
| `INGEST_CONCURRENCY` | 4 | Tasks in flight at once |
| `INGEST_ACK_WAIT` | `2m` | How long JetStream waits for a heartbeat before it redelivers |
| `LOG_LEVEL` | `info` | trace, debug, info, warn or error |
| `ADMIN_ADDR` | `:9090` | Serves `/metrics` and `/debug/pprof/` inside the compose network |

## Testing

- `make test` runs the unit tests.
- `make smoke`, with the stack up, publishes a real task for the `power-delhi-monthly` bundle and checks the `StageDone` and the files in the bucket. Its gateway part also uploads a CSV, runs a job on it and checks the cleaned-up file, and checks that a broken CSV fails with the row to fix.
- `make smoke-power` runs a real job through NASA POWER at a point near Lisbon. It needs no token, but CI skips it so CI doesn't depend on NASA.
- `make smoke-harmony` runs a real GPM job through the gateway and Harmony. It needs `EARTHDATA_TOKEN` in `.env`, so CI doesn't run it.
- `make smoke-giovanni` runs twenty years of daily groundwater at a point in Punjab through the gateway and Giovanni, and checks the grid cell and the rows. It needs `EARTHDATA_TOKEN` too.
