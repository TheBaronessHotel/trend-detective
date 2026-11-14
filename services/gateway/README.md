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
| `internal/events` | The consumer of `events.stage.done` |

## API

So far: `ListVariables`, `CreateJob`, `GetJob` and `WatchJob`. The rest return `unimplemented` until their own tasks: `GetResultURL` and `CreateUploadURL` (GW-5), `Narrate` (GW-6), and `GetQuota`.

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

## How a job runs

1. `CreateJob` checks the request, fills in defaults, stores the job and publishes the ingest task.
2. Every `StageDone` is recorded, and its task is counted once. JetStream delivers each event at least once, so a Lua script makes the count safe to repeat.
3. When the last task of a stage reports:
   - if any task failed, the job fails;
   - if the stage is the last one, `stop_after` or analysis, the job succeeds;
   - otherwise the gateway builds the next stage's tasks from the results and publishes them.

Task IDs are predictable, like `<job>-preprocess-0`. If the gateway crashes halfway through starting a stage, the redelivered event starts it again with the same IDs, and JetStream drops the repeated publishes by `Nats-Msg-Id`.

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
| `NATS_URL`, `REDIS_URL` | required | Set by compose |
| `HTTP_ADDR` | `:8080` | |
| `CORS_ORIGINS` | `*` | Comma-separated origins allowed to call the API from a browser |
| `SUBREGION_N` | 8 | Default sub-regions per side |
| `MAX_GRID` | 128 | Largest grid side. A request can lower it, never raise it |
| `MAX_GRANULES` | 1000 | Passed to Harmony tasks |
| `LOG_LEVEL`, `ADMIN_ADDR` | `info`, `:9090` | As for every Go service |

## Testing

- `make test` covers the catalog, and the orchestrator against an in-memory Redis: every stage, duplicates, late duplicates, failures, `stop_after`, the token handling, `WatchJob`, the advisory mapping and the debug page.
- `make smoke`, with the stack up, creates a real job that stops after ingest, follows it with `WatchJob` until it succeeds, and checks `/debug/jobs`.
