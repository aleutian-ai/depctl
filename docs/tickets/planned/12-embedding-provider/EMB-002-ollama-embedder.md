# EMB-002: Ollama embedder

**Epic:** Embedding Provider
**Status:** planned
**Depends on:** EMB-001
**Estimated size:** medium

## Goal
Implement `Embedder` against a local Ollama HTTP endpoint, as the reference/default embedding provider for v0.1.

## Non-goals
- No OpenAI-compatible or Sentence-Transformers adapters yet — those are later, optional adapters once the Ollama reference implementation and its tests prove the interface.

## Simplicity constraints
- One small hand-written HTTP client using `net/http` — no generated SDK, no third-party Ollama client dependency.
- Batching: send a bounded number of texts per request per config (`embedding.batch_size` or similar), sequential requests — no concurrent request fan-out in v0.1; the config's `workers.embed` limit can gate this later if needed, don't build it preemptively.

## Design
- Package: `internal/embedding/ollama`.
- Config (from `internal/config`):
```yaml
embedding:
  provider: ollama
  endpoint: http://127.0.0.1:11434
  model: nomic-embed-text
```
- `type Client struct { endpoint string; model string; httpClient *http.Client }` implementing `embedding.Embedder`.
- `Embed` posts to `POST {endpoint}/api/embed` (Ollama's embeddings endpoint) with `{"model": ..., "input": [...]}`, parses response vectors.
- `Dimensions(ctx)` derived from a one-time probe embed call (embed a fixed short string, measure vector length) cached after first call.
- Context cancellation: pass `ctx` through to the HTTP request (`http.NewRequestWithContext`).
- Retry: retry transient HTTP errors (5xx, connection refused, timeout) with a small fixed number of attempts and short backoff (e.g. 3 attempts, exponential 200ms/400ms/800ms) — no generic retry framework, just a small loop in this package.

## Inputs / Outputs
- Input: `[]string` chunk texts, model config.
- Output: `[][]float32` vectors, or error after retries exhausted.

## Failure behavior
- Non-transient errors (4xx, malformed response) fail immediately, no retry.
- Dimension mismatch between probe and later batches is a hard error surfaced to the caller.

## Tests
- Fake HTTP server (`httptest.NewServer`) returning canned embed responses — verify request shape and response parsing.
- Transient failure then success — verify retry occurs and result is correct.
- Non-transient 400 — verify no retry, immediate error.
- Optional live integration test gated behind an env var (e.g. `RAGCTL_TEST_OLLAMA=1`), skipped by default in CI.

## Acceptance criteria
- [ ] Fake-server unit tests pass without any live Ollama instance.
- [ ] Context cancellation aborts an in-flight embed call.
