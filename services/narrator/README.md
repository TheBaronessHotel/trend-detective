# narrator

Go. Streams an LLM explanation of finished analysis results, using the user's own key. The LLM only explains numbers our code computed.

See the Narrator section of [ARCHITECTURE.md](../../ARCHITECTURE.md).

## How it works

`NarratorService.Narrate` is a server stream. For each call the narrator:

1. Validates the request: `result.series` is set and the question and history fit the limits (4000 characters, 40 messages, 64000 characters of history).
2. Reads the key from the `x-llm-api-key` header. The key stays in memory for the call and is never logged or echoed in an error. With no key, an empty question and a `llm.base_url` that isn't local, it skips the LLM and streams the template overview (see below). Otherwise it checks that `llm.provider` and `llm.model` are set and that `llm.base_url` is one this server may call (see [Which addresses it calls](#which-addresses-it-calls)). The key may be empty only when `llm.base_url` is local: localhost, a loopback or private IP, `host.docker.internal`, or a single-label host like `ollama`. A question without a key gets `unauthenticated`. A key is only sent over plain `http` to a local host; a remote `http://` base URL with a key gets `invalid_argument`.
3. Loads the `SeriesAnalysis`, and the `GridAnalysis` when `result.grid` is set, from the `analysis` bucket. Refs to any other bucket are refused. A missing or broken grid only drops the spatial part.
4. Builds the LLM request with `prompt.Builder`, picks an `llm.Client` for the provider and streams the answer as `text_delta` events.
5. Ends with `done`: the model that answered, plus `unverified_numbers` from `verify.Verifier` over the full answer.

If the stream fails after some text was sent, it ends with the error and no `done`.

### Which addresses it calls

`llm.base_url` comes from the browser, so the narrator checks it before and while calling:

- It never calls link-local addresses (`169.254.0.0/16`, where cloud metadata lives, and `fe80::/10`), unspecified, multicast or broadcast addresses, or the metadata addresses `fd00:ec2::254` and `100.100.100.200`, whatever the allowlist says.
- It doesn't call the stack's own services (`seaweedfs`, `nats`, `redis`, `gateway`, ...) unless `NARRATOR_LLM_BASE_URL_ALLOWLIST` names them exactly.
- With a non-empty allowlist, the host must match it.
- `internal/egress` checks the address actually dialed: a name that looks public (it has a dot) must resolve to a public address, unless the allowlist lists it. So `127.0.0.1.nip.io` or a DNS answer that changes after the check can't reach the internal network. Local names and IP literals may be internal, for Ollama and LM Studio.
- Redirects are not followed.

### Through a proxy

The LLM client honours `HTTPS_PROXY`, `HTTP_PROXY` and `NO_PROXY`. With a proxy, the narrator dials the proxy, which is exempt from the address check, and checks the target host before the request goes out. A name the narrator can't resolve itself is left to the proxy.

In compose, set `NARRATOR_HTTPS_PROXY` (and `NARRATOR_HTTP_PROXY` for `http://` endpoints) in `.env`. For a proxy on the host, use `http://host.docker.internal:<port>`. The proxy has to listen on the Docker bridge, not only on 127.0.0.1: bind it to `0.0.0.0` or the bridge address (often `172.17.0.1`), and let the firewall accept connections from the Docker network. `NARRATOR_NO_PROXY` defaults to the stack's services, localhost and `host.docker.internal`, so a local Ollama doesn't go through the proxy.

### Without a key

The app works without an LLM key. For an overview request (empty question) with no key and a non-local `llm.base_url`, `internal/overview` writes 3 to 5 plain sentences from the results alone: direction and rate per decade with the confidence interval, significance (p-value against alpha, and whether Mann-Kendall agrees), record length, the main warnings and a significant Pettitt change point, and a reminder that a trend is not proof of a cause. It streams one sentence per `text_delta`, then `done` with `model: "template"` and no `unverified_numbers`. The `llm` config may be missing and isn't checked, since nothing is called.

### Errors

| Failure | Connect code |
|---|---|
| Bad request, bad model or base URL (`llm.ErrBadRequest`), a remote `http://` base URL with a key | `invalid_argument` |
| The base URL redirects (`llm.ErrRedirect`) | `invalid_argument` |
| A question with no key for a public endpoint, key rejected (`llm.ErrUnauthorized`) | `unauthenticated` |
| Provider rate limit (`llm.ErrRateLimited`) | `resource_exhausted` |
| No credits or quota left (`llm.ErrQuotaExhausted`: OpenAI `insufficient_quota`, Anthropic billing, OpenRouter 402) | `resource_exhausted`, with "This LLM key has no credits left" |
| Provider down or failing (`llm.ErrUpstream`) | `unavailable` |
| No response headers within `NARRATOR_LLM_HEADER_TIMEOUT` (`llm.ErrSlowStart`) | `deadline_exceeded` |
| Longer than `NARRATE_TIMEOUT` | `deadline_exceeded` |
| The caller went away | `canceled` |
| `llm.base_url` host not in `NARRATOR_LLM_BASE_URL_ALLOWLIST`, a forbidden or internal address (see above) | `permission_denied` |
| Series results missing from the bucket | `not_found` |
| Object store unreachable or failing mid-read | `unavailable` |
| Anything else | `internal`, detail in the log only |

When the provider explains a failure in a JSON error body (`{"error":{"message":...}}` from OpenAI, OpenRouter and Gemini, its array-wrapped form, or Anthropic's `{"type":"error",...}`), the message ends with "Provider said: " and the provider's words: the key and key-like tokens removed, control characters stripped, at most 300 characters. Other bodies are never shown, since a base URL could point at a service that isn't an LLM. Authentication errors never show the provider's text, as it can quote part of the key.

Layout:

```
cmd/narrator/main.go     config, obs, Connect server with h2c, /healthz, graceful shutdown
cmd/narrator/wiring.go   wires prompt.NewBuilder, verify.NewVerifier and llm.New (no redirects)
internal/server          the Narrate handler, request checks, error mapping
internal/egress          which addresses the LLM client may dial, proxy-aware
internal/overview        the template overview used without a key
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
| `NARRATE_TIMEOUT` | `3m` | Upper bound for one call, stream included. Keep the gateway's `NARRATOR_TIMEOUT` longer, so this error reaches the browser |
| `NARRATOR_LLM_HEADER_TIMEOUT` | `150s` | How long the provider may take before its response headers. Thinking models can hold them back for minutes. Capped at `NARRATE_TIMEOUT`. Past it the user reads that the provider took too long to start answering |
| `NARRATOR_LLM_BASE_URL_ALLOWLIST` | empty | Comma-separated hosts `llm.base_url` may point at, like `localhost,ollama,openrouter.ai,*.example.org`. An entry matches the hostname, or `host:port` if it has a port; an entry without a port allows every port of that host. `*.x` matches subdomains of x. Empty allows any host except the forbidden and internal ones above. An empty `base_url` (the provider's own endpoint) is always allowed. A listed name may resolve to an internal address. Refused hosts get `permission_denied` before any client is created |
| `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY` | empty | Outbound proxy for LLM calls, see [Through a proxy](#through-a-proxy). Compose fills them from `NARRATOR_HTTPS_PROXY`, `NARRATOR_HTTP_PROXY` and `NARRATOR_NO_PROXY` |
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
