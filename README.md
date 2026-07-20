# mortise

A minimal, self-hosted AI gateway in Go. One OpenAI-compatible socket for vLLM
fleets and external endpoints — routing, failover, rate limits, token
accounting, and OTel tracing. No agent logic, no bloat.

mortise has zero knowledge of agents, sessions, prompts, or guardrails. It is a
thin, observable reverse proxy: OpenAI-compatible in, OpenAI-compatible out.

## Features (v0)

- **OpenAI-compatible ingress** — `POST /v1/chat/completions` (incl. SSE
  streaming), `GET /v1/models`, `GET /healthz`.
- **Model-name routing** — map a client-visible model to a backend pool.
- **Retries & failover** — try a pool's backends in order; fail over on
  transport errors and retryable statuses (408/429/5xx).
- **Per-key limits** — token-bucket RPS limiting and rolling per-minute token
  budgets.
- **Token accounting** — reads `usage` from responses (incl. streaming with
  `stream_options.include_usage`).
- **Request timeouts** — per-attempt deadlines, configurable per pool.
- **Idempotency dedup** — clients sending `Idempotency-Key` get the original
  response replayed instead of a second upstream call, so retrying agents are
  never double-billed.
- **OpenTelemetry** — a span and metrics (requests, errors, retries, duration,
  tokens) per request, exported via OTLP/gRPC.

## Quick start

```sh
go build -o mortise ./cmd/mortise
cp mortise.example.yaml mortise.yaml   # edit pools, routes, keys
./mortise -config mortise.yaml
```

```sh
curl localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-mortise-abc123" \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"model":"llama-3.1-8b","messages":[{"role":"user","content":"hi"}]}'
```

## Configuration

A single YAML file drives everything: backend pools, model→pool routes, client
API keys with limits, timeouts, and telemetry. See
[`mortise.example.yaml`](./mortise.example.yaml) for a fully commented example.

## Layout

```
cmd/mortise            entrypoint: load config, wire, serve
internal/config        single-file YAML config + validation
internal/openai        minimal OpenAI request/response shapes
internal/server        HTTP handler wiring the pipeline together
internal/auth          API-key ingress auth
internal/router        model-name -> pool resolution
internal/proxy         egress: retries, failover, per-attempt timeouts
internal/ratelimit     per-key RPS + token accounting
internal/dedupe        idempotency-key store (leader/replay)
internal/telemetry     OpenTelemetry traces + metrics
```

## Releases & versioning

mortise uses [Conventional Commits](https://www.conventionalcommits.org/) with
[release-please](https://github.com/googleapis/release-please) for automated
semantic versioning. On merge to `main`, release-please maintains a release PR
that bumps the version and updates `CHANGELOG.md`; merging it tags `vX.Y.Z` and
creates a GitHub Release. [GoReleaser](https://goreleaser.com) then builds and
attaches cross-platform binaries. The running version is embedded at build time:

```sh
mortise -version   # mortise vX.Y.Z (commit <sha>, built <date>)
```

## Non-goals

Prompt logic, guardrails, caching beyond dedupe, and any coupling to specific
agent frameworks. Egress is OpenAI-compatible backends only.

## Development

```sh
go test ./...
```
