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
| `internal/harmony` | The `harmony-subset` adapter |
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

## Failures

- A transient error, like a network failure or a NASA 5xx, NAKs the message with a 10 second backoff, then 1 minute. After 3 failed attempts in total, the task is reported failed.
- Any other error publishes a `StageDone` with a `TaskError` and acks.
- Failed attempts are counted in Redis, not by JetStream. Every Harmony poll is a JetStream delivery too, so the consumer's delivery limit is only a ceiling, at 2000.
- While a task runs, the worker sends a heartbeat every third of `INGEST_ACK_WAIT`, so a long download isn't redelivered.

## Settings

| Variable | Default | |
|---|---|---|
| `NATS_URL`, `REDIS_URL`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | required | Set by compose |
| `EARTHDATA_TOKEN` | empty | The team's token, from `.env`. Without it, Harmony tasks fail unless the user gave their own |
| `HARMONY_CONCURRENCY` | 4 | Harmony jobs in flight per token |
| `HARMONY_URL` | `https://harmony.earthdata.nasa.gov` | |
| `CMR_URL` | `https://cmr.earthdata.nasa.gov` | |
| `BUNDLE_DIR` | `/bundle` | Compose mounts `services/ingest/bundle` here |
| `INGEST_CONCURRENCY` | 4 | Tasks in flight at once |
| `INGEST_ACK_WAIT` | `2m` | How long JetStream waits for a heartbeat before it redelivers |
| `LOG_LEVEL` | `info` | trace, debug, info, warn or error |
| `ADMIN_ADDR` | `:9090` | Serves `/metrics` and `/debug/pprof/` inside the compose network |

## Testing

- `make test` runs the unit tests.
- `make smoke`, with the stack up, publishes a real task for the `power-delhi-monthly` bundle and checks the `StageDone` and the files in the bucket.
- `make smoke-harmony` runs a real GPM job through the gateway and Harmony. It needs `EARTHDATA_TOKEN` in `.env`, so CI doesn't run it.
