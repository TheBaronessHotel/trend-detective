# analysis

Python 3.14. Runs the trend tests on the region series preprocess wrote, and writes a `SeriesAnalysis` document to the `analysis` bucket. It reads `gateway.v1.Task` messages from `tasks.analysis` and reports a `StageDone` for each one on `events.stage.done`.

See the Analysis section of [ARCHITECTURE.md](../../ARCHITECTURE.md), and the [science notes](https://app.notion.com/p/41897b633bd6819384e5d98df3e80c02) for where the methods come from.

## Status

ANA-1 is in: regression, Mann-Kendall with Sen's slope, and the agreement check. So is ANA-4: the shuffle test, the trend triangle, the monthly trends and the years to significance. Still to come:

- **ANA-2, the grid tests.** `TestPlan.grid` is logged and skipped, and `AnalysisResult.grid` stays unset.
- **ANA-3, diagnostics.** Pettitt, residual checks and outliers are logged and skipped.

Skipped tests leave their fields unset, so the web app and the narrator should check a field is there before using it.

## Layout

| Path | What it is |
|---|---|
| `src/analysis/__main__.py` | Wiring: settings, logging, S3, the worker |
| `src/analysis/worker.py` | The JetStream consumer. Acks only after its `StageDone` is out |
| `src/analysis/pipeline.py` | One task: cache check, read the `Series`, run the suite, write. Knows nothing about NATS |
| `src/analysis/series.py` | Puts a `Series` on a regular monthly calendar and picks the field to test |
| `src/analysis/suite.py` | Runs the plan's series tests and builds `SeriesAnalysis`, with the caveats |
| `src/analysis/regression.py` | Least squares with the effective sample size correction |
| `src/analysis/kendall.py` | Mann-Kendall (original, Hamed and Rao, seasonal), Sen's slope and its interval |
| `src/analysis/extras.py` | The shuffle test, the trend triangle, the monthly trends and the years to significance |
| `src/analysis/storage.py` | S3 through boto3, plus an in-memory store for tests |

## What the tests run on

- **Monthly values only.** A daily or weekly `Series` fails with `INVALID_REQUEST`, because the tests, the seasonal cycle and the autocorrelation corrections all assume months. Daily sources become monthly once preprocess runs `monthly_mean` (PRE-2).
- **A regular calendar.** The values go onto one slot per month from the first step to the last. A month missing from `Series.times` counts as missing, the same as a NaN. `n_total` is the number of slots and `n_valid` the slots with a value.
- **Anomalies by default.** `TestPlan.field` unset means `SERIES_FIELD_ANOMALIES`. When the `Series` has anomalies, the tests use them, and `units` becomes `%` for percent anomalies or `standard deviations` for standardized ones.
- **Anomalies made here when preprocess has none.** Until PRE-2 brings the `anomaly` step, every `Series` arrives without anomalies. Analysis then subtracts each calendar month's mean over the whole record, which needs that month in at least 2 years, and says so in `warnings`. These are always absolute anomalies, whatever the catalog's anomaly kind is. Once preprocess sends anomalies, they're used instead and the caveat goes away.
- **Seasonal Kendall runs on the tested field too**, although the proto says it uses the raw values. It compares each calendar month only with itself, and an anomaly is its value shifted or scaled by that month's normal, which keeps the order within a month. So S, z and p are the same either way, and with absolute anomalies so is the slope. With percent or standardized anomalies the slope comes out in their units, like every other slope in the document. The comment on `MANN_KENDALL_VARIANT_SEASONAL` should say so the next time the protos are regenerated.
- **Enough data.** At least 12 values spread over 2 years, or the task fails with `NO_DATA`.

## Methods

Slopes in the document are per decade, in `units`. Time is counted in months, and a year is exactly 12 of them.

**Regression** (`RegressionResult`). Least squares of the series on time, with a t-test on the slope. With `correct_autocorrelation`, the test uses the effective sample size of Santer et al. (2000), `n_eff = n (1 - r1) / (1 + r1)`, where `r1` is the lag-1 autocorrelation of the residuals. The standard errors grow by `sqrt((n - 2) / (n_eff - 2))`, and the t distribution has `n_eff - 2` degrees of freedom. Only a positive `r1` is corrected. `r1` pairs only months that are real neighbors, so a gap doesn't join two distant months. For the test, `n_eff` is floored at 3 so there's a degree of freedom left. The reported `n_effective` isn't floored.

**Mann-Kendall** (`MannKendallResult`). The S statistic with the tie correction and the continuity correction, and a two-sided normal p-value. The variant comes from the plan, and unset means Hamed and Rao.

- `ORIGINAL`: Mann (1945) and Kendall (1975).
- `HAMED_RAO`: the variance of S is multiplied by Hamed and Rao's (1998) factor, from the autocorrelations of the ranks of the series detrended with Sen's slope. Only autocorrelations outside the 1 - alpha band around zero count. As in pymannkendall, the ranks are taken with the gaps squeezed out.
- `SEASONAL`: Hirsch, Slack and Smith (1982). S and its variance are summed over the 12 calendar months.

`tau` is S over the number of pairs compared. Sen's slope is the median of the pairwise slopes, all pairs for the first two variants and pairs within a calendar month for the seasonal one. Pairs use the real times, so a gap doesn't make the line look steeper. Its confidence interval is the Sen (1968) and Gilbert (1987) method that `scipy.stats.theilslopes` uses, from the variance of S of the test that ran. Under Hamed and Rao that variance includes the autocorrelation, so the interval widens with it.

The unit tests check all three variants against pymannkendall and the interval against scipy, on synthetic series and on the Delhi bundle.

**Agreement.** `same_direction` compares the signs of the regression slope and Sen's slope, and `same_significance` their verdicts. It's set when both tests ran.

### Extras

They test the same field as the regression. Where they need a p-value, they use the least-squares t-test with the same autocorrelation correction as the plan's regression, or with the correction on when the plan has no regression.

**Shuffle test** (`ShuffleResult`). The series is cut into blocks of `block_length` months, 12 by default, counted from its first month. The whole blocks change places while the times stay put, and the least-squares slope is refitted each time, `permutations` times, 1000 by default. Blocks keep the autocorrelation inside them, and with 12-month blocks every value stays in its calendar month. A tail shorter than a block stays at the end.

- `p_value` is `(k + 1) / (N + 1)`, where k of the N shuffles are at least as steep as the real slope. Counting the real series as one of the shuffles keeps it above 0.
- `shuffled_slopes_per_decade` holds every shuffled slope, for the histogram, and `observed_slope_per_decade` is the regression's slope.
- The shuffles come from a fixed seed, so a repeat task gives the same histogram.
- It needs 5 whole blocks, since fewer can't be put in enough orders. A shorter record skips it with a caveat. `permutations` can be 1 to 10,000.

**Trend triangle** (`TrendTriangleResult`). The trend of every window of whole calendar years at least `min_years` long, 10 by default. It shows whether a trend depends on where the record starts and ends. Rows are `start_years` and columns `end_years`, and both matrices are row-major `[start][end]`. A cell is NaN when its window is shorter than `min_years` or has fewer than 12 values. The cell for the first and the last year is the whole record, the same as `regression`. A record spanning fewer calendar years than `min_years` skips it with a caveat.

**Monthly trends** (`monthly_trends`). A least-squares trend for each calendar month from its values across the years, January first. A month seen in fewer than 5 years gets NaN. When one or two months come out significant, a caveat says that twelve separate tests can produce that by chance: 46% of trendless series show at least one significant month at alpha 0.05, but only 2% show three.

**Years to significance** (`YearsToSignificanceResult`). Weatherhead et al. (1998): the years of monthly data needed to detect the fitted trend with 90% probability, `n* = [3.3 * noise_sd / |trend_per_year| * sqrt((1 + phi) / (1 - phi))] ^ (2/3)`. `noise_sd` is the spread of the regression's residuals and `phi` their lag-1 autocorrelation, which the formula clips to ±0.99. A flat line gives `Infinity`. `years_so_far` runs from the first valid month to the last, and `already_significant` is the regression's verdict.

## Drawing the trend line and its band

`RegressionResult.model` is enough to draw both, with no extra fields:

- The line is `intercept + slope * t`, with `t` in years since `time_origin` and `slope` per year. Analysis counts a year as 12 months. Turning a date into `t` with years of 365.25 days is off by a couple of days at most, which no chart shows.
- `time_origin` is the mean time of the valid months, so the intercept and the slope are uncorrelated. The half-width of the confidence band at `t` is `t_crit * sqrt(intercept_stderr^2 + (t * slope_stderr)^2)`.
- `t_crit` can be read back from the slope's interval: `t_crit = (slope_ci_high - slope_per_decade) / (10 * slope_stderr)`.
- The standard errors already include the autocorrelation correction, so the band widens with it too.

## Caveats in `warnings`

Plain sentences for the user and the narrator. In order:

| When | Text, roughly |
|---|---|
| Analysis made the anomalies | Each value was compared with the average for its month of the year... |
| `field` is `VALUES` | The tests ran on the raw values, so the seasons are still in them |
| Under 10 years from the first value to the last | Only N years of data. A trend in a record this short is uncertain |
| Over 20% of the months missing | N% of the months have no value |
| `n_eff` under 10, with the correction on | Neighboring months are so alike that the N values count as only about M independent ones... |
| Only one of regression and Mann-Kendall is significant | Only one of the two tests finds the trend significant... |
| Both are significant, in opposite directions | The two tests find significant trends in opposite directions... |
| Fewer than 5 whole shuffle blocks | The record is too short for the shuffle test, which needs at least N months |
| Fewer calendar years than the triangle's `min_years` | The record is shorter than the N years the trend triangle's shortest window needs |
| One or two months significant on their own | Each month of the year was tested on its own, and with twelve tests one or two can look significant just by chance |

Two tests that both find nothing get no caveat, even when their small slopes point different ways.

## Where files go

Each task has a cache key: a SHA-256 of the `AnalysisTask` plus `CODE_VERSION` in `pipeline.py`. The input's keys change whenever the data does, because preprocess keys its documents by its own inputs. Files land in the `analysis` bucket under the key:

```
analysis/<cache key>/series.json    SeriesAnalysis, as protojson
analysis/<cache key>/result.json    AnalysisResult, written last
```

If `result.json` exists, the task reuses it and reports `cache_hit: true`. Bump `CODE_VERSION` whenever the output for the same task could change.

## Failures

| Error | Code |
|---|---|
| A daily or weekly series, more than one value in a month, a bad `alpha`, an unknown variant, or shuffle or triangle settings out of range | `INVALID_REQUEST` |
| Fewer than 12 values, or less than 2 years between the first and the last | `NO_DATA` |
| A `Series` that isn't valid protojson | `INTERNAL` |
| SeaweedFS errors, including a missing `Series` | Retried after 10 s, then 1 min, and reported as `INTERNAL` after 3 attempts |
| Anything else | `INTERNAL` |

## Settings

| Variable | Default | |
|---|---|---|
| `NATS_URL`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | required | Set by compose |
| `ANALYSIS_CONCURRENCY` | 2 | Tasks in flight at once |
| `ANALYSIS_ACK_WAIT` | `2m` | A heartbeat goes out every third of this while a task runs |
| `LOG_LEVEL` | `info` | trace, debug, info, warn or error |

Logs are `key=value` lines with `time`, `level` and `msg`, plus `job_id`, `task_id` and `trace_id` inside a task, the same as preprocess and the Go services.

## Testing

- `make test-analysis` runs what CI's `analysis` workflow runs, in a few seconds: the lockfile check, ruff with bug rules only, and the unit tests. It needs uv.
- pymannkendall is a dev dependency only. The service doesn't import it.
- A calibration test runs the shuffle test on 100 trendless autocorrelated series and expects about 5% false alarms. The same series give a plain t-test about 21%.
- `make smoke`, with the stack up, runs the Delhi bundle through every stage and checks the trend analysis has the regression, Mann-Kendall and extras results.
