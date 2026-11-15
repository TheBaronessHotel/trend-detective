# preprocess

Python 3.14. Decodes the raw files ingest stored into `Series` and `Grid` documents in the `prep` bucket. It reads `gateway.v1.Task` messages from `tasks.preprocess` and reports a `StageDone` for each one on `events.stage.done`.

See the Preprocessing and Regions and grids sections of [ARCHITECTURE.md](../../ARCHITECTURE.md).

## Status

PRE-1 (decoders) and PRE-2 (the filter chain) are in. Still to come:

- **PRE-3, aggregation.** A grid becomes a region series by a cos(latitude)-weighted mean of its valid pixels, for values and anomalies separately, so the region anomaly is already the mean of the per-pixel anomalies.
  - The `Grid` averages square blocks of `k x k` native pixels the same way: weighted, per-pixel anomalies, and a cell is NaN when fewer than `min_valid_fraction` of its pixels that ever have data are valid. `output.block_size` picks `k`: `block_pixels`, else `ceil(longer side / subregion_n)`, else 1, then at least 3 cells on the shorter side and at most `max_grid` per side. Blocks at the bottom and right edges keep only their real pixels.
  - `Grid.block_pixels` is the `k` used, `cell_pixels` counts each cell's pixels that ever have data (0 for an all-ocean cell), and `lat_edges` and `lon_edges` give each cell's exact extent. The region series doesn't depend on `k`.
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
| `src/preprocess/filters/` | The filter chain: the registry and chain checks, the pixel steps, and normals and anomalies |
| `src/preprocess/output.py` | Builds `Series`, `Grid` and the series CSV |
| `src/preprocess/storage.py` | S3 through boto3, plus an in-memory store for tests |

## The canonical form

Every decoder returns a `Decoded`: an xarray Dataset with a `value` variable (float64, NaN where missing) and a `reason` variable (int8 `MissingReason` codes) over the same dims.

- Dims are `(time,)` for points or `(time, lat, lon)` for grids.
- Before the filter chain, two more things can appear, and the chain must remove both:
  - a `level` dim between `time` and `lat`: pressure levels in hPa, for profiles like AIRS methane. `column_average` collapses it, and data still on levels after the chain fails with `UNSUPPORTED`;
  - helper variables named `helper:<variable path>`, like AIRS surface pressure, which a step names. They're dropped after the chain.
- `time` is the start of each step in UTC. Monthly times are snapped to the 1st at 00:00, because products stamp months differently, like MERRA-2's 00:30.
- Latitude runs north first and longitude west first, in -180 to 180.
- Units are the task's `source_units` when set. A file that says otherwise is logged, because the catalog's unit filters are written against `source_units`.

After decoding, the pipeline keeps the steps inside the requested dates and the pixels whose centers fall inside the rectangle or on its edges, within 1e-6° so rounding in the files' coordinates doesn't drop an edge (MERRA-2's 0° east arrives as -5.9e-13). Edge pixels can reach half a pixel past the rectangle. A rectangle smaller than one pixel keeps the pixel it touches.

The time step comes from `RawDataset.step` when ingest set it, as csv-upload does, and otherwise from the spacing of the dates. Monthly series get every month from first to last, with months a source skipped marked missing, so a step's position always says how far apart two steps are.

## The filter chain

The catalog gives each variable an ordered list of steps, and the gateway copies it into every task. The steps act at three points, whatever their place in the list:

1. **Pixel steps** change the values in list order.
2. **`anomaly`** adds a per-pixel anomaly layer next to the values, which stay as they are.
3. **`gap_fill_display`** fills `Series.display_values` on the finished region series.

| Step | What it does | Marks missing as |
|---|---|---|
| `qc_range{min, max}` | Drops values outside the range. Uses the source's units, so it comes before `linear` | `QC_FAILED` |
| `linear{scale, offset, units}` | `value × scale + offset`, then renames the units. An unset scale means 1 | |
| `monthly_mean{min_valid_days}` | The mean of each month's valid days. A weekly value counts as 7 days. Monthly data passes through | `TOO_FEW_DAYS` |
| `log10` | Units become `log₁₀(<units>)`, as the catalog writes them | `NOT_POSITIVE` |
| `column_average{surface_pressure, min_coverage}` | Collapses data on pressure levels into the column average above each pixel, each level weighted by the air it holds: its layer runs halfway to the levels on either side, cut at the surface pressure (a helper variable) and 0 hPa at the top. Under 90% of the column's air with a value, by default, is missing. A surface pressure above 1200 hPa can't be hPa and fails the task | `QC_FAILED` |
| `coarsen{factor}` | Deprecated for grids: it averages blocks before the region series and the anomaly step, unweighted. The `Grid`'s blocks replace it. Point data passes through | |
| `anomaly{kind, baseline, min_baseline_coverage, method, harmonics}` | The difference from each calendar month's normal, per pixel | |
| `gap_fill_display{max_gap}` | Draws straight lines across gaps of up to `max_gap` steps (default 2), never at the ends | |

Anomalies:
- **Baseline:** the step's years, or the whole record, clipped to the data. `Series.baseline` says which were used.
- **Normals:** the mean of each calendar month, or a fit of `harmonics` sine and cosine pairs (default 2). A month's normal counts only with data in at least `min_baseline_coverage` (default 0.7) of the baseline years it appears in.
- **Trend:** either way a straight-line trend is fitted along with the seasons over the baseline, and the normals are the seasons at the baseline's mean time. A plain mean of each calendar month would take in the trend's rise within the year, since December comes later than January, and leave the anomalies a staircase from year to year: a trend about `1 / Y^2` low over Y years, 2% over 7, and for a strong trend like CO2 twice the scatter around it. Analysis checked this against NOAA's published CO2 growth, see Validation in [its README](../analysis/README.md). The deviations behind standardized anomalies stay the plain spread of each calendar month.
- **Kinds:** absolute (the default); percent, skipped where a normal is under 1% of that pixel's largest monthly normal and refused for temperatures; standardized, skipped where a month's deviation is zero.
- **Missing anomalies:** a step whose value is missing has no anomaly. A missing anomaly next to a present value always means that calendar month has `usable: false` in `Series.normals`.

Chain checks fail the task with `INVALID_REQUEST`: an empty step, `anomaly`, `monthly_mean`, `gap_fill_display` or `column_average` twice, a `column_average` without levels or without its surface pressure helper, a pixel step after `anomaly`, or a `qc_range` whose max is below its min or whose limits are both 0. proto3 reads an unset limit as 0, so a `qc_range` needs both limits. One with only `min` set below 0 can't be caught and drops every value above 0.

Daily or weekly data whose chain has no `monthly_mean` gets `monthly_mean{min_valid_days: 20}` before its first step other than `qc_range` or `linear`. Anomalies need calendar months, and a daily user CSV can't say so in the catalog.

`tests/test_catalog.py` runs every chain in `services/gateway/catalog.yaml` on synthetic data and checks the units come out as the catalog shows them, so a catalog change preprocess can't carry out fails CI.

## Decoders

| Format | From | Notes |
|---|---|---|
| `FORMAT_POWER_JSON` | `power`, `bundle` | Drops the 13th "month", the annual mean. `-999` is missing |
| `FORMAT_NETCDF4` | `harmony-subset` | `Grid/precipitation` means group `Grid`, variable `precipitation`. Handles `(time, lon, lat)` storage, 0-360 longitudes, fill values and packing. An axis with no values in the variable's own group takes them from the nearest group above that has a variable of that name, as AIRS keeps `YDim_ascending` and `XDim_ascending` at the root. `YDim`/`XDim` are latitude and longitude, and a `*PressureLev*` axis, or one in hPa or Pa, is a pressure `level`. A latitude, longitude or level axis with no coordinate values anywhere fails with `UPSTREAM`, rather than placing the map at index positions. A single pressure level is dropped, like any axis of size 1. A time axis in months or years since a date, like NOBM's `months since 1998-01-01`, which xarray won't decode, is read as numbers and turned into the first of each month. The first of `RawDataset.variables` is the measurement; any others are read as helpers, and a helper whose units say Pa becomes hPa. Each file is opened once, and coordinates from parent groups are read straight through netCDF4, so an AIRS subset decodes in about 0.15 s. A granule without a time axis takes its date from ingest's coverage |
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
| `PREPROCESS_CONCURRENCY` | 2 | Tasks in flight at once, in threads. The netCDF and HDF5 C libraries aren't thread-safe, so file reads take turns behind one lock in `decoders/netcdf.py`; without it, two tasks reading at once crashed the process with a segfault |
| `PREPROCESS_ACK_WAIT` | `2m` | A heartbeat goes out every third of this while a task runs |
| `LOG_LEVEL` | `info` | trace, debug, info, warn or error |

Logs are `key=value` lines with `time`, `level` and `msg`, plus `job_id`, `task_id` and `trace_id` inside a task, in the same format and with the same field names as the Go services.

## Testing

- `make test-preprocess` runs what CI's `preprocess` workflow runs, in a few seconds: the lockfile check, ruff with bug rules only, and the unit tests. It needs uv.
- The unit tests also import the worker and every module in `gen/python`, and check that each `ingest.v1.Format` an adapter can produce has a decoder. A new format in the proto fails that test until it has one.
- `tests/test_catalog.py` runs every filter chain in the catalog. The workflow also runs when `services/gateway/catalog.yaml` changes.
- `tests/test_airs.py` decodes `tests/fixtures/airs3stm-2020-02-delhi.nc4`, a trimmed copy of a real Harmony subset of AIRS3STM v7 (February 2020, 27-29 N, 76-78 E) that keeps the layout that matters: coordinates at the root, data in `ascending/Data_Fields`. It also computes the methane column from it, about 1830 ppb.
- `make smoke-airs` runs every AIRS variable live through all stages. It needs `EARTHDATA_TOKEN` in `.env`.
- `make smoke`, with the stack up, runs the Delhi bundle through the gateway, ingest and preprocess, then downloads the series and checks it has 528 months.
