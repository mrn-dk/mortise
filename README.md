# mortise

**A minimal, self-hosted OpenAI-compatible AI gateway in Go.**

One socket fronts your vLLM fleets and external OpenAI-compatible endpoints,
adding routing, failover, rate limits, token accounting, idempotent replay, and
OpenTelemetry — and nothing else. mortise has no knowledge of agents, sessions,
prompts, or guardrails: OpenAI-compatible in, OpenAI-compatible out.

```
          ┌────────────────────────── mortise ──────────────────────────┐
client ──▶│ auth ─▶ rate limit ─▶ route ─▶ dedupe ─▶ retry/failover ─▶   │──▶ vLLM / OpenAI
  (Bearer)│                                          (per-attempt timeout)│    (pool of backends)
          └──────────────────────── OTel traces + metrics ───────────────┘
```

## Why

Running several inference backends (vLLM nodes, plus an OpenAI fallback) means
every client needs to know URLs, keys, and which model lives where. mortise
collapses that into **one endpoint and one key**: clients call a stable model
name, mortise routes to a healthy backend, retries on failure, enforces
per-key quotas, and emits telemetry — without touching the payload.

## Features

| Capability            | Detail                                                                                     |
| --------------------- | ------------------------------------------------------------------------------------------ |
| OpenAI-compatible     | `POST /v1/chat/completions` (incl. SSE streaming), `GET /v1/models`, `GET /healthz`        |
| Model-name routing    | Map a client-visible model to a backend **pool**                                           |
| Retries & failover    | Try a pool's backends in order; fail over on transport errors and 408/429/5xx              |
| Backoff               | Exponential backoff with full jitter between attempts, honoring `Retry-After`              |
| Per-key limits        | Token-bucket RPS (`golang.org/x/time/rate`) + sliding-window per-minute token budgets      |
| Token accounting      | Reads `usage` from responses, incl. streaming via `stream_options.include_usage`           |
| Idempotent replay     | `Idempotency-Key` replays the original response instead of re-billing a duplicate call     |
| Telemetry             | A span + metrics (requests, errors, retries, duration, tokens) per request via OTLP/gRPC   |
| Interactive API docs  | Swagger UI at `/docs`, OpenAPI spec at `/openapi.yaml`                                      |

## Quick start

```sh
go build -o mortise ./cmd/mortise
cp mortise.example.yaml mortise.yaml   # edit pools, routes, keys
./mortise -config mortise.yaml
```

Send a request (the `Idempotency-Key` is optional but recommended for agents
that retry):

```sh
curl localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-mortise-abc123" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"model":"llama-3.1-8b","messages":[{"role":"user","content":"hi"}]}'
```

## API docs

mortise serves interactive documentation directly from the binary — no external
files to deploy:

| Path            | Description                                    |
| --------------- | ---------------------------------------------- |
| `/docs`         | Swagger UI (try requests in the browser)       |
| `/openapi.yaml` | The raw OpenAPI 3.0 specification              |

Open <http://localhost:8080/docs> after starting the server. The spec is
embedded via `go:embed` ([`internal/server/openapi.yaml`](./internal/server/openapi.yaml)).

## API summary

All `/v1/*` endpoints require `Authorization: Bearer <key>`.

- **`POST /v1/chat/completions`** — OpenAI-compatible. Bodies are forwarded
  verbatim; mortise only inspects `model`, `stream`, and `usage`. Set
  `stream: true` for SSE. Send `Idempotency-Key` to make retries replay the
  original response (marked `Idempotency-Replayed: true`).
- **`GET /v1/models`** — lists the routable model names.
- **`GET /healthz`** — unauthenticated liveness probe (`200 ok`).

Errors use the OpenAI error envelope: `401` (bad key), `404` (no route for
model), `429` (rate/token limit), `502` (all upstreams failed).

## Configuration

A single YAML file drives everything: backend pools, model→pool routes, client
API keys with limits, timeouts, idempotency caps, and telemetry. `${VAR}`
references are expanded from the environment at load. See
[`mortise.example.yaml`](./mortise.example.yaml) for a fully commented example.

```yaml
pools:
  - name: llama-fleet
    retries: 2
    timeout: 60s
    backends:
      - { name: vllm-a, base_url: http://vllm-a:8000/v1 }
      - { name: vllm-b, base_url: http://vllm-b:8000/v1 }

routes:
  - { model: llama-3.1-8b, pool: llama-fleet }

keys:
  - { name: team-agents, key: sk-mortise-abc123, rps: 20, tokens_per_min: 200000 }
```

## Architecture

The request pipeline is a small chain of single-purpose packages:

```
cmd/mortise            entrypoint: load config, wire, serve, graceful shutdown
internal/config        single-file YAML config + validation + env expansion
internal/openai        minimal OpenAI request/response shapes
internal/server        HTTP handler wiring the pipeline + embedded API docs
internal/auth          API-key ingress auth (digest lookup)
internal/router        model-name -> pool resolution
internal/proxy         egress: tuned transport, retries, failover, backoff, timeouts
internal/ratelimit     per-key RPS (x/time/rate) + sliding-window token accounting
internal/dedupe        idempotency-key store: sharded, LRU-bounded, leader/replay
internal/telemetry     OpenTelemetry traces + metrics
```

Design notes:

- **Streaming without buffering.** Responses stream straight to the client;
  bytes are only retained when captured for idempotent replay (and that capture
  is size-capped), so unbounded SSE streams don't grow the heap.
- **No global locks on the hot path.** Rate-limit state is built per key at
  startup; the dedupe store is sharded. Neither serializes requests on a single
  mutex.
- **Tuned egress.** A dedicated `http.Transport` raises the stdlib's
  2-idle-conns-per-host default so a busy gateway keeps connections warm.

## Releases & versioning

mortise uses [Conventional Commits](https://www.conventionalcommits.org/) with
[release-please](https://github.com/googleapis/release-please) for automated
semantic versioning. On merge to `main`, release-please maintains a release PR
that bumps the version and updates `CHANGELOG.md`; merging it tags `vX.Y.Z` and
creates a GitHub Release, which [GoReleaser](https://goreleaser.com) fills with
cross-platform binaries. The running version is embedded at build time:

```sh
mortise -version   # mortise vX.Y.Z (commit <sha>, built <date>)
```

## Development

```sh
go test -race ./...
```

## Non-goals

Prompt logic, guardrails, response caching beyond idempotent dedupe, and any
coupling to specific agent frameworks. Egress is OpenAI-compatible backends only.
```
