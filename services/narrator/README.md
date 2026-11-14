# narrator

Go. Streams an LLM explanation of finished analysis results, using the user's own key. The LLM only explains numbers our code computed.

See the Narrator section of [ARCHITECTURE.md](../../ARCHITECTURE.md).

## How it works

`NarratorService.Narrate` is a server stream. For each call the narrator:

1. Validates the request: `result.series` is set, `llm.provider` and `llm.model` are set, `llm.base_url` is http(s) and its host passes `NARRATOR_LLM_BASE_URL_ALLOWLIST`, and the question and history fit the limits (4000 characters, 40 messages, 64000 characters of history).
2. Reads the key from the `x-llm-api-key` header. It may be empty only when `llm.base_url` is local: localhost, a loopback or private IP, `host.docker.internal`, or a single-label host like `ollama`. The key stays in memory for the call and is never logged or echoed in an error.
3. Loads the `SeriesAnalysis`, and the `GridAnalysis` when `result.grid` is set, from the `analysis` bucket. Refs to any other bucket are refused. A missing or broken grid only drops the spatial part.
4. Builds the LLM request with `prompt.Builder`, picks an `llm.Client` for the provider and streams the answer as `text_delta` events.
5. Ends with `done`: the model that answered, plus `unverified_numbers` from `verify.Verifier` over the full answer.

If the stream fails after some text was sent, it ends with the error and no `done`.

| Failure | Connect code |
|---|---|
| Bad request, bad model or base URL (`llm.ErrBadRequest`) | `invalid_argument` |
| No key for a public endpoint, key rejected (`llm.ErrUnauthorized`) | `unauthenticated` |
| Provider rate limit (`llm.ErrRateLimited`) | `resource_exhausted` |
| Provider down or failing (`llm.ErrUpstream`) | `unavailable` |
| `llm.base_url` host not in `NARRATOR_LLM_BASE_URL_ALLOWLIST` | `permission_denied` |
| Series results missing from the bucket | `not_found` |
| Object store unreachable or failing mid-read | `unavailable` |
| Anything else | `internal`, detail in the log only |

Layout:

```
cmd/narrator/main.go     config, obs, Connect server with h2c, /healthz, graceful shutdown
cmd/narrator/wiring.go   wires prompt.NewBuilder, verify.NewVerifier and llm.New (no redirects)
internal/server          the Narrate handler, request checks, error mapping
internal/llm             LLM client contract and adapters
internal/prompt          builds the LLM request from the results
internal/verify          finds numbers in the answer that aren't in the results
```

## Configuration

| Variable | Default | What it does |
|---|---|---|
| `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | required | SeaweedFS S3 |
| `ANALYSIS_BUCKET` | `analysis` | Where results are read from |
| `HTTP_ADDR` | `:8081` | Connect API and `/healthz` |
| `ADMIN_ADDR` | `:9090` | `/metrics` and pprof |
| `NARRATE_TIMEOUT` | `3m` | Upper bound for one call, stream included |
| `NARRATOR_LLM_BASE_URL_ALLOWLIST` | empty | Comma-separated hosts `llm.base_url` may point at, like `localhost,ollama,openrouter.ai,*.example.org`. An entry matches the hostname, or `host:port` if it has a port; `*.x` matches subdomains of x. Empty allows any host. An empty `base_url` (the provider's own endpoint) is always allowed. Refused hosts get `permission_denied` before any client is created |
| `LOG_LEVEL` | `info` | `trace`, `debug`, `info`, `warn` or `error` |

There is no key setting on purpose. Every call brings the user's own key.

## Running it

With the stack: `make up` builds and starts it on http://localhost:8081. `docker compose logs -f narrator` follows its logs.

On its own, against the stack's SeaweedFS:

```
S3_ENDPOINT=http://localhost:8333 S3_ACCESS_KEY=trenddetective S3_SECRET_KEY=localdevsecret \
  HTTP_ADDR=:8081 ADMIN_ADDR=:9091 go run ./services/narrator/cmd/narrator
```

Try a call with curl. Connect streams need a 5-byte envelope before the JSON body:

```
body='{"result":{"series":{"bucket":"analysis","key":"<job>/series.json"}},"llm":{"provider":"PROVIDER_OPENAI_CHAT","model":"gpt-5.5"}}'
{ printf '00%08x' ${#body} | xxd -r -p; printf %s "$body"; } | \
  curl -sN -X POST http://localhost:8081/trenddetective.narrator.v1.NarratorService/Narrate \
    -H 'Content-Type: application/connect+json' -H "x-llm-api-key: $OPENAI_API_KEY" --data-binary @-
```

For Ollama on your machine, use `"base_url":"http://host.docker.internal:11434/v1"` (or `http://localhost:11434/v1` outside Docker) with `PROVIDER_OPENAI_CHAT` and no key.

## Tests

```
go test ./services/narrator/...
```

The handler tests run a real Connect server on httptest with fakes for the loader, builder, LLM client and verifier, so they need no network, SeaweedFS or key. `TestNarrateEndToEnd` wires the real loader, `prompt.NewBuilder`, `verify.NewVerifier` and `llm.New` against a fake object store and fake OpenAI and Anthropic SSE servers.
