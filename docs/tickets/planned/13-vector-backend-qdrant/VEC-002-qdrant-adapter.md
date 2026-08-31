# VEC-002: Qdrant adapter

**Epic:** Vector Backend (Qdrant)
**Status:** planned
**Depends on:** VEC-001
**Estimated size:** medium

## Goal
Implement `VectorBackend` against Qdrant's HTTP API as the first, reference vector backend adapter.

## Non-goals
- No other backends (Weaviate, Milvus, Chroma, pgvector) — those come in milestone 24, only after this adapter and its conformance tests exist.
- No gRPC Qdrant client — HTTP only, to keep the dependency footprint small.

## Simplicity constraints
- Use a small hand-written HTTP client (`net/http` + `encoding/json`) rather than a generated Qdrant SDK, per the plan's explicit "do not require a generated client if a small HTTP client keeps dependency weight lower."
- One collection per `ragctl` install (configurable name), filtered by metadata for ecosystem/package/version/generation — do not create one Qdrant collection per dependency version.

## Design
- Package: `internal/backend/qdrant`.
- Config:
```yaml
vector:
  backend: qdrant
  endpoint: http://127.0.0.1:6333
  collection: ragctl
  batch_size: 256
```
- `type Client struct { endpoint, collection string; httpClient *http.Client }` implementing `backend.VectorBackend`.
- Operations map to Qdrant HTTP endpoints:
  - `EnsureNamespace` → `PUT /collections/{collection}` (create if not exists, idempotent).
  - `Upsert` → `PUT /collections/{collection}/points` with payload containing metadata fields (ecosystem, dependency, version, generation, source_type, authority) plus point ID (deterministic, derived from chunk ID) and vector.
  - `Delete` → `POST /collections/{collection}/points/delete` by point IDs or by filter.
  - `Query` → `POST /collections/{collection}/points/search` with vector + metadata filter (must-match on ecosystem/dependency/version/generation as requested).
  - `Health` → `GET /collections/{collection}` or `GET /healthz`.
- Batch upserts at configured `batch_size`.

## Inputs / Outputs
- Input: `UpsertRequest`/`QueryRequest` from `internal/backend` interface.
- Output: acknowledgement / `QueryResult` with matched points + metadata + score.

## Failure behavior
- HTTP errors surfaced as typed `ErrBackendUnavailable` (5xx/connection errors, retryable by caller) vs `ErrBackendRequest` (4xx, non-retryable).
- `EnsureNamespace` is idempotent — calling it on an existing collection with the same config is a no-op, not an error.

## Tests
- testcontainers-go integration test spinning up real Qdrant: create collection, upsert two versions of the same package's chunks, query with a version filter, assert only the requested version's points are returned.
- Unit tests for request/response marshaling against a fake HTTP server (no container needed for these).

## Acceptance criteria
- [ ] testcontainers integration test passes: inserting two versions of same package, filtered query returns only the requested version.
- [ ] `EnsureNamespace` is safely callable multiple times.
