# Changelog

## 0.1.0 (2026-07-22)


### Features

* **auth:** API-key bearer ingress authentication ([4d2e339](https://github.com/mrn-dk/mortise/commit/4d2e3391e538eaa4877316bd917311a22a2b0211))
* **cmd:** add -version flag with ldflags-injected build info ([126f05d](https://github.com/mrn-dk/mortise/commit/126f05d9a9174d7bd94465943c792f27e1c9a316))
* **cmd:** mortise entrypoint with graceful shutdown ([b36dc5b](https://github.com/mrn-dk/mortise/commit/b36dc5b04307cb389fe141e5871261fb12bb7c31))
* **config:** expand env vars and add idempotency cache limits ([61b63ef](https://github.com/mrn-dk/mortise/commit/61b63efa1a42685e3a399fa60a3bf7e86ff6f4ea))
* **config:** single-file YAML config with defaults and validation ([383514f](https://github.com/mrn-dk/mortise/commit/383514ffd3d9cc416774613be141744d9dc4f316))
* **dedupe:** idempotency-key store with leader/replay dedup ([e3bba99](https://github.com/mrn-dk/mortise/commit/e3bba9972cd2b59bf2d3d2b942d65997f82fdddc))
* **openai:** minimal OpenAI request and response types ([d84ddf1](https://github.com/mrn-dk/mortise/commit/d84ddf1ea398029898e56d80270a783bf40e0ce3))
* **proxy:** egress with retries, failover, and per-attempt timeouts ([67b2605](https://github.com/mrn-dk/mortise/commit/67b2605aae93d676daa044db8f6453273612f37f))
* **ratelimit:** per-key token-bucket RPS and token accounting ([5be5040](https://github.com/mrn-dk/mortise/commit/5be50409c7242a673ed2d7d80e10a77a90531d4a))
* **router:** model-name to pool routing ([bd206b6](https://github.com/mrn-dk/mortise/commit/bd206b6f770b664d402636f3dfa57daf57c7662e))
* **server:** OpenAI-compatible HTTP server wiring the pipeline ([1383271](https://github.com/mrn-dk/mortise/commit/138327113fe9ffa09f579ed2306cf76c3999150c))
* **server:** serve OpenAPI spec and Swagger UI docs ([1a651e0](https://github.com/mrn-dk/mortise/commit/1a651e04a41b1a85fb58a7936af0e955da596db6))
* **telemetry:** OpenTelemetry traces and metrics via OTLP ([fb792a6](https://github.com/mrn-dk/mortise/commit/fb792a66c7f94ef9166a47a4eae4aa4a6e894c21))


### Bug Fixes

* **auth:** look up API keys by digest to avoid timing side-channel ([9111d3c](https://github.com/mrn-dk/mortise/commit/9111d3c89f932b4eebdec79073f400fc74d6ca85))


### Performance Improvements

* **dedupe:** shard the store and bound it with LRU eviction ([f33345b](https://github.com/mrn-dk/mortise/commit/f33345b7ebf1a60c90ec90e733f0922044ab531e))
* **proxy:** tune egress transport and add backoff with Retry-After ([caaaac3](https://github.com/mrn-dk/mortise/commit/caaaac3e01f04fe26593c5a8a533303b68485dd6))
* **ratelimit:** drop global mutex; use x/time/rate and a sliding window ([21e6c53](https://github.com/mrn-dk/mortise/commit/21e6c5390ce269153acdf642110fa03472057511))
* **server:** stream without buffering and memoize metric attributes ([5d5b445](https://github.com/mrn-dk/mortise/commit/5d5b445e2403501a01585185b8a4d329256a5cac))


### Miscellaneous Chores

* release 0.1.0 ([e77fb9b](https://github.com/mrn-dk/mortise/commit/e77fb9b411cedab6c050b3fdb3fda24726d0395c))
