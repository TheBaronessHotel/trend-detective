# preprocess

Python 3.14. Decodes the raw files ingest stored into `Series` and `Grid` documents in the `prep` bucket. It reads `gateway.v1.Task` messages from `tasks.preprocess` and reports a `StageDone` for each one on `events.stage.done`.

See the Preprocessing and Regions and grids sections of [ARCHITECTURE.md](../../ARCHITECTURE.md).

## Status

PRE-1 is in: the service runs, decodes every format ingest produces today, and writes a passthrough result so the walking skeleton works end to end. Still to come:

- **PRE-2, the filter chain.** The task's `filters` are logged and skipped, so values go out in the source's units.
- **PRE-3, aggregation.** A grid becomes a region series by a plain cos(latitude)-weighted mean of its valid pixels. Anomalies per pixel, normals and the N×N sub-regions come with PRE-3.
  - Coverage counts only pixels that have data at least once in the record, so ocean in a land-only product like GLDAS works as a mask instead of failing every month of a coastal rectangle.
  - `Grid.bbox` is the requested rectangle, the same as `Series.bbox`.
- **PRE-4, the slider cubes.** `value_cube` and `anomaly_cube` stay unset.

## Layout

| Path | What it is |
|---|---|
| `src/preprocess/__main__.py` | Wiring: settings, logging, S3, the worker |
| `src/preprocess/worker.py` | The JetStream consumer. Acks only after its `StageDone` is out |
| `src/preprocess/pipeline.py` | One task: cache check, download, decode, trim, write. Knows nothing about NATS |
| `src/preprocess/canonical.py` | The in-memory form every decoder returns, and the time and axis helpers |
| `src/preprocess/decoders/` | One module per `ingest.v1.Format` |
| `src/preprocess/output.py` | Builds `Series`, `Grid` and the series CSV |
| `src/preprocess/storage.py` | S3 through boto3, plus an in-memory store for tests |

## The canonical form

Every decoder returns a `Decoded`: an xarray Dataset with a `value` variable (float64, NaN where missing) and a `reason` variable (int8 `MissingReason` codes) over the same dims.

- Dims are `(time,)` for points or `(time, lat, lon)` for grids.
- `time` is the start of each step in UTC. Monthly times are snapped to the 1st at 00:00, because products stamp months differently, like MERRA-2's 00:30.
- Latitude runs north first and longitude west first, in -180 to 180.
- Units are the task's `source_units` when set. A file that says otherwise is logged, because the catalog's unit filters are written against `source_units`.

After decoding, the pipeline keeps the steps inside the requested dates and the pixels whose centers fall inside the rectangle. A rectangle smaller than one pixel keeps the pixel it touches.

## Decoders

| Format | From | Notes |
|---|---|---|
| `FORMAT_POWER_JSON` | `power`, `bundle` | Drops the 13th "month", the annual mean. `-999` is missing |
| `FORMAT_NETCDF4` | `harmony-subset` | `Grid/precipitation` means group `Grid`, variable `precipitation`. Handles `(time, lon, lat)` storage, 0-360 longitudes, fill values and packing |
| `FORMAT_USER_CSV` | `csv-upload` | The first column of dates and the first mostly-numeric column. Comma, semicolon or tab |
| `FORMAT_GIOVANNI_CSV` | `giovanni-area` | `key:,value` lines, a blank line, then `time,mean_<id>`. Empty values are missing, and timestamps may use a space instead of `T` |
| `FORMAT_GIOVANNI_POINT_CSV` | `giovanni-point` | `key,value` lines, a blank line, then `Timestamp (UTC),Data`. The header's `undef` value, like `-9999` or `-1.26765e+30`, is missing. The header's `lat` and `lon` give the grid cell |
| `FORMAT_GEOTIFF` | `appeears` | Not yet. Fails with `UNSUPPORTED` |

## Where files go

Each task has a cache key: a SHA-256 of the `PreprocessTask`, without `fetched_at`, plus `CODE_VERSION` in `pipeline.py`. Files land in the `prep` bucket under it:

```
prep/<cache key>/series.json        Series, as protojson
prep/<cache key>/series.csv         the same series, for download
prep/<cache key>/grid.json          Grid, for gridded sources
prep/<cache key>/grid-values.f32    little-endian float32, [time][row][col]
prep/<cache key>/result.json        PreprocessResult, written last
```

If `result.json` exists, the task reuses it and reports `cache_hit: true`. Bump `CODE_VERSION` whenever the output for the same task could change.

## Failures

| Error | Code |
|---|---|
| Unknown format, a missing variable, a CSV without dates | `INVALID_REQUEST` or `UNSUPPORTED` |
| Every value missing after trimming | `NO_DATA` |
| A file that can't be read | `UPSTREAM` |
| SeaweedFS errors | Retried after 10 s, then 1 min, and reported as `INTERNAL` after 3 attempts |
| Anything else | `INTERNAL` |

The consumer's delivery limit is 3. Preprocess never polls, so JetStream's delivery count is the attempt number. Ingest needs a Redis counter only because its Harmony polls count as deliveries.

## Settings

| Variable | Default | |
|---|---|---|
| `NATS_URL`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | required | Set by compose |
| `PREPROCESS_CONCURRENCY` | 2 | Tasks in flight at once |
| `PREPROCESS_ACK_WAIT` | `2m` | A heartbeat goes out every third of this while a task runs |
| `LOG_LEVEL` | `info` | trace, debug, info, warn or error |

Logs are `key=value` lines with `time`, `level` and `msg`, plus `job_id`, `task_id` and `trace_id` inside a task, in the same format and with the same field names as the Go services.

## Testing

- `make test-preprocess` runs what CI's `preprocess` workflow runs, in a few seconds: the lockfile check, ruff with bug rules only, and the unit tests. It needs uv.
- The unit tests also import the worker and every module in `gen/python`, and check that each `ingest.v1.Format` an adapter can produce has a decoder. A new format in the proto fails that test until it has one.
- `make smoke`, with the stack up, runs the Delhi bundle through the gateway, ingest and preprocess, then downloads the series and checks it has 528 months.
