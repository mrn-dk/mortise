# mortise

**A minimal, self-hosted OpenAI-compatible AI gateway in Go.**

mortise puts one OpenAI-compatible socket in front of your vLLM fleets and
external endpoints. Clients call a stable model name with one API key; mortise
routes to a healthy backend, retries and fails over, enforces per-key rate and
token limits, replays idempotent retries so agents aren't double-billed, and
emits OpenTelemetry — all without touching the payload. It has no knowledge of
agents, sessions, prompts, or guardrails: OpenAI-compatible in, out.

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

Then open <http://localhost:8080/docs> for the interactive API reference.

## API

All `/v1/*` endpoints require `Authorization: Bearer <key>`.

- `POST /v1/chat/completions` — OpenAI-compatible. Set `stream: true` for SSE.
  Send `Idempotency-Key` to make retries replay the original response.
- `GET /v1/models` — list the routable model names.
- `GET /healthz` — unauthenticated liveness probe.
- `GET /docs`, `GET /openapi.json` — Swagger UI and the OpenAPI spec, generated
  from the handler annotations by [swaggo/swag](https://github.com/swaggo/swag)
  (`go generate ./...`).

## Configuration

A single YAML file drives everything: backend pools, model→pool routes, client
API keys with limits, timeouts, and telemetry. `${VAR}` references are expanded
from the environment. See [`mortise.example.yaml`](./mortise.example.yaml) for a
fully commented example.

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
  - { name: team-agents, key_sha256: 08025e34…42be1, rps: 20, tokens_per_min: 200000 }
```

## Secrets

The config carries two kinds of secret; keep neither in plaintext in version control:

- **Client keys** (verified, not reproduced) — store the SHA-256 digest via
  `key_sha256` so the raw token never touches disk. Generate one with
  `printf %s "$TOKEN" | sha256sum`. (`key:` plaintext still works for local dev.)
- **Backend `api_key`** (sent upstream verbatim, so it can't be hashed) — inject
  it at runtime with `${ENV}` expansion from your secret manager, e.g.
  `api_key: ${OPENAI_API_KEY}`.

mortise warns if the config file is group/other-readable — keep it `0600`. Your
live `mortise.yaml` is git-ignored; only `mortise.example.yaml` is committed.

## Development

```sh
go test -race ./...
go generate ./...   # regenerate the OpenAPI docs after changing handlers
```
