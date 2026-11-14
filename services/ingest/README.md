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

## Failures

- A transient error, like a network failure or a NASA 5xx, NAKs the message with a 10 second backoff, then 1 minute. JetStream redelivers it up to 3 times in total.
- Any other error, or the last attempt, publishes a `StageDone` with a `TaskError` and acks.
- While a task runs, the worker sends a heartbeat every third of `INGEST_ACK_WAIT`, so a long download isn't redelivered.

## Settings

| Variable | Default | |
|---|---|---|
| `NATS_URL`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | required | Set by compose |
| `BUNDLE_DIR` | `/bundle` | Compose mounts `services/ingest/bundle` here |
| `INGEST_CONCURRENCY` | 4 | Tasks in flight at once |
| `INGEST_ACK_WAIT` | `2m` | How long JetStream waits for a heartbeat before it redelivers |
| `LOG_LEVEL` | `info` | trace, debug, info, warn or error |
| `ADMIN_ADDR` | `:9090` | Serves `/metrics` and `/debug/pprof/` inside the compose network |

## Testing

- `make test` runs the unit tests.
- `make smoke`, with the stack up, publishes a real task for the `power-delhi-monthly` bundle and checks the `StageDone` and the files in the bucket.
