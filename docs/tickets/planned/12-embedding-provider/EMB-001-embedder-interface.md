# EMB-001: Embedder interface

**Epic:** Embedding Provider
**Status:** planned
**Depends on:** CHUNK-001
**Estimated size:** small

## Goal
Define the narrow `Embedder` interface that all embedding providers implement, plus the persisted embedding-identity metadata needed to avoid mixing incompatible vectors.

## Non-goals
- No concrete provider implementation (EMB-002).
- No caching (EMB-003).

## Simplicity constraints
- Exactly one interface, no provider registry/factory abstraction until a second provider is actually being built. A `switch` on config string is enough for v0.1 wiring in `cmd/ragctl`.
- Do not add streaming or async embedding APIs — batch request/response is sufficient.

## Design
- Package: `internal/embedding`.
```go
type Embedder interface {
    Name() string
    ModelID() string
    Dimensions(ctx context.Context) (int, error)
    Embed(ctx context.Context, texts []string) ([][]float32, error)
}
```
- Embedding identity struct persisted alongside vectors/generations:
```go
type EmbeddingIdentity struct {
    Provider      string
    Model         string
    Dimensions    int
    Normalization string
    CreatedAt     time.Time
}
```
- A generation's vector replica is always tied to one `EmbeddingIdentity`; changing provider/model must never silently reuse a prior replica (see VEC-003, EMB-003).

## Inputs / Outputs
- Input: chunk text content.
- Output: one float32 vector per input text, in the same order.

## Failure behavior
- `Embed` returns an error for the whole batch on any failure (no partial-batch semantics in v0.1) — callers retry the batch.
- `Dimensions` must be checked against `EmbeddingIdentity.Dimensions` by callers before writing to a backend; mismatch is a caller-side hard error, not silently coerced.

## Tests
- Interface compiles against a fake in-memory embedder used by downstream tests (VEC-*, VAL-*).

## Acceptance criteria
- [ ] `internal/embedding` package defines `Embedder` and `EmbeddingIdentity` with no provider-specific code.
