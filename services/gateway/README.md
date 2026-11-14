# gateway

Go. The public Connect API on port 8080, and the orchestrator of every job. It publishes stage tasks on NATS, reads `StageDone` events from the EVENTS stream, and is the only service that writes job state to Redis.

## Layout

| Path | What it is |
|---|---|
| `cmd/gateway` | Wiring: settings, the-watchers, Redis, NATS, the HTTP server |
| `catalog.yaml` | The variable catalog, built into the binary. The header comment explains every field |
| `internal/catalog` | Loads and checks the catalog. A broken catalog stops the gateway at startup |
| `internal/jobs` | The orchestrator, and the Redis store it uses |
| `internal/api` | The Connect handlers. They turn errors into codes the web app can show |
| `internal/narrate` | The `Narrate` proxy to the narrator service |
| `internal/events` | The consumer of `events.stage.done` |

## API

So far: `ListVariables`, `CreateJob`, `GetJob`, `WatchJob`, `GetResultURL`, `CreateUploadURL` and `Narrate`. `GetQuota` still returns `unimplemented`.

`WatchJob` sends the job right away and again after every change, then ends the stream once the job succeeds or fails. Every write to a job wakes its watchers at once, and they also re-read the job every 5 seconds in case a wake-up was missed. In the browser, connect-es handles the stream. `deploy/smoke/watch.py` is a small client that uses only the Python standard library.

Connect speaks plain JSON over HTTP, so curl works:

```sh
api=http://localhost:8080/trenddetective.gateway.v1.GatewayService

curl -s -H 'Content-Type: application/json' -d '{}' $api/ListVariables

curl -s -H 'Content-Type: application/json' -d '{"spec": {
  "variableId": "air-temperature-power",
  "bbox": {"west": 77, "south": 28, "east": 78, "north": 29},
  "dates": {"start": {"year": 1981, "month": 1, "day": 1}, "end": {"year": 2024, "month": 12, "day": 31}},
  "stopAfter": "STAGE_INGEST"}}' $api/CreateJob

curl -s -H 'Content-Type: application/json' -d '{"jobId": "<id from CreateJob>"}' $api/GetJob
```

Only `variableId`, `bbox` and `dates` are required. The gateway fills in the rest from the catalog, and `GetJob` shows the spec it actually used. An `X-Earthdata-Token` header on `CreateJob` makes ingest use that user's own NASA quota. The token goes into Redis, and only its key goes over NATS.

## Narrate

`Narrate(job_id, question, history, llm)` streams the narrator's explanation of a finished job. The user's LLM key goes in the `X-Llm-Api-Key` header. Without a key, an empty question gets the narrator's template overview.

1. The gateway loads the job. Until `results.analysis.series` is set, it answers `failed_precondition` (HTTP 409) with a reason: still running, failed, or stopped before analysis.
2. It builds the narrator's `NarrateRequest`: the analysis refs from the job, the `VariableContext` from the catalog (id, name, definition, units, `llm_hints`), bbox and dates from the job's resolved spec, and the browser's question, history and `llm` as sent.
3. It calls the narrator at `NARRATOR_URL` over h2c, forwarding the key header only when one was sent, and relays every event unchanged.

The narrator's own errors keep their code and message, like `unauthenticated` for a rejected key or `resource_exhausted` for a rate limit. If the narrator can't be reached, the answer is `unavailable` and the rest of the app works as usual. Compose has no `depends_on` on the narrator for that reason. The key is held for the call only. It is never logged, and it is removed from any narrator error before that error is logged or passed on.

Connect streams need a 5-byte envelope before the JSON body:

```sh
body='{"jobId":"<id>","llm":{"provider":"PROVIDER_OPENAI_CHAT","model":"gpt-5.5"}}'
{ printf '00%08x' ${#body} | xxd -r -p; printf %s "$body"; } | \
  curl -sN -H 'Content-Type: application/connect+json' -H "X-Llm-Api-Key: $OPENAI_API_KEY" \
    --data-binary @- $api/Narrate
```

## How a job runs

1. `CreateJob` checks the request, fills in defaults, runs the preflight checks below, stores the job and publishes the ingest task.
2. Every `StageDone` is recorded, and its task is counted once. JetStream delivers each event at least once, so a Lua script makes the count safe to repeat.
3. When the last task of a stage reports:
   - if any task failed, the job fails;
   - if the stage is the last one, `stop_after` or analysis, the job succeeds;
   - otherwise the gateway builds the next stage's tasks from the results and publishes them.

Task IDs are predictable, like `<job>-preprocess-0`. If the gateway crashes halfway through starting a stage, the redelivered event starts it again with the same IDs, and JetStream drops the repeated publishes by `Nats-Msg-Id`.

## Preflight checks

Before queuing anything, `CreateJob` refuses a job that can't work, with a message the user can act on:

- **Dates.** Dates partly outside the variable's `available` range are clamped to it, and `GetJob` shows the dates used. Dates entirely outside it are refused, with the range in the message.
- **Harmony sources:**
  - **Granule count:** CMR counts the granules. Zero is refused as "no data". More than `MAX_GRANULES` is refused with the count, so the user can shorten the range.
  - **Capabilities:** once every 6 hours per collection, Harmony's capabilities must show a service that subsets by area, time and variable.
- **Giovanni sources.** The job is refused once the quota used reaches `QUOTA_CUTOFF`. The check uses the user's token, or `EARTHDATA_TOKEN`. The refusal comes back as `resource_exhausted`, and every other refusal as `invalid_argument`.
- **Task cap.** A job that plans more tasks than `TASK_CAP` is refused.

The NASA checks fail open. If CMR, Harmony or Giovanni can't answer within 5 seconds, the gateway logs a warning and the job goes ahead, and ingest's own checks apply.

## Downloads and uploads

Files go straight between the browser and SeaweedFS through presigned URLs, so they never pass through the gateway.

- **`GetResultURL(job_id, artifact)`** signs a download for one output: the series, the series CSV, a value or anomaly cube, or the series or grid analysis.
  - For a cube, `data_url` is the cube's binary data. The gateway reads the cube header to find it.
  - An output the job doesn't have yet gets `failed_precondition`, with a message like "The trend analysis isn't ready yet."
  - Only the prep and analysis buckets are ever signed.
- **`CreateUploadURL(filename, size_bytes)`** signs a PUT for a CSV of up to `MAX_UPLOAD_MB`, at `uploads/<uuid>/<cleaned-up name>`. Pass the returned `upload` as `JobSpec.upload`. A presigned PUT can't enforce the size, so the csv-upload adapter checks it again when reading. `CreateJob` refuses an upload ref outside the `uploads` bucket. The `user-csv` catalog entry runs CSV jobs.

URLs are signed for `S3_PUBLIC_ENDPOINT`, the address the browser uses, and work for `PRESIGN_TTL`. The signature covers the host, so a URL signed for the internal `seaweedfs:8333` would fail in the browser. SeaweedFS answers the browser's CORS preflight itself, using `S3_ALLOWED_ORIGINS`.

## When a stage never reports

If a stage crashes on every attempt, it never sends a `StageDone`. Once its message runs out of deliveries, NATS publishes a max-deliveries advisory, and the ADVISORIES stream keeps it. The gateway then:
1. reads the task from TASKS;
2. records it as failed with `RETRIES_EXHAUSTED`, which fails the job;
3. deletes the dead message.

## Debugging a job

`GET http://localhost:8080/debug/jobs/<id>` returns the job, with every task's stage, attempts and errors, plus the raw `StageDone` events behind it, as indented JSON. Compose publishes the gateway on 127.0.0.1 only.

## Redis layout

Every key of a job shares the `{id}` hash tag, and everything expires after 7 days.

| Key | Type | Holds |
|---|---|---|
| `td:job:{id}` | hash | spec, state, current stage, results, timestamps, task counts |
| `td:job:{id}:tasks` | hash | task ID to `TaskStatus` |
| `td:job:{id}:done` | hash | task ID to its first `StageDone` |
| `td:job:{id}:stage:<n>` | hash | `total` and `finished` for that stage |
| `td:job:{id}:earthdata` | string | a user's own Earthdata token |

## Adding a variable

Add an entry to `catalog.yaml` and run `make test`. The catalog test fails on unknown fields, missing definitions, a default source that isn't offered, and filter or test settings that don't match the protos.

## Settings

| Variable | Default | |
|---|---|---|
| `NATS_URL`, `REDIS_URL`, `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | required | Set by compose |
| `S3_PUBLIC_ENDPOINT` | `http://localhost:8333` | The S3 address the browser uses. URLs are signed for it |
| `PRESIGN_TTL` | `15m` | How long signed URLs work |
| `MAX_UPLOAD_MB` | 20 | Largest CSV upload |
| `HTTP_ADDR` | `:8080` | |
| `CORS_ORIGINS` | `*` | Comma-separated origins allowed to call the API from a browser |
| `SUBREGION_N` | 8 | Default sub-regions per side |
| `MAX_GRID` | 128 | Largest grid side. A request can lower it, never raise it |
| `MAX_GRANULES` | 1000 | Most granules a Harmony job may fetch. Checked in preflight and again in ingest |
| `TASK_CAP` | 256 | Most tasks one job may plan |
| `QUOTA_CUTOFF` | 0.9 | Share of the Giovanni quota after which Giovanni jobs are refused |
| `EARTHDATA_TOKEN` | empty | The team's token, for the Giovanni quota check |
| `CMR_URL`, `HARMONY_URL`, `GIOVANNI_URL` | NASA's public addresses | |
| `NARRATOR_URL` | `http://narrator:8081` | The narrator's Connect address, called over h2c |
| `NARRATOR_TIMEOUT` | `4m` | Upper bound for one `Narrate` call, stream included. Keep it above the narrator's `NARRATE_TIMEOUT` (3m) so its own timeout reaches the user |
| `LOG_LEVEL`, `ADMIN_ADDR` | `info`, `:9090` | As for every Go service |

## Testing

- `make test` covers the catalog, and the orchestrator against an in-memory Redis: every stage, duplicates, late duplicates, failures, `stop_after`, the token handling, `WatchJob`, the advisory mapping, the debug page, signed downloads and uploads, the preflight checks against a fake NASA, date clamping and the task cap. `Narrate` runs against a fake narrator served over h2c: the relayed stream, the request it builds, key forwarding, unfinished jobs, narrator errors, an unreachable narrator, the timeout, and that the key never reaches a log.
- `make smoke`, with the stack up, creates a real job that stops after ingest, follows it with `WatchJob` until it succeeds, and checks `/debug/jobs`. It also checks that the job has no series, and uploads a CSV through a signed URL.
