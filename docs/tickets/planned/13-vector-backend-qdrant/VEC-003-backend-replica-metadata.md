# VEC-003: Backend replica metadata

**Epic:** Vector Backend (Qdrant)
**Status:** planned
**Depends on:** VEC-002, STORE-001
**Estimated size:** small

## Goal
Persist a `BackendReplica` record in bbolt tracking, per (generation, backend, embedding model), the replication status and point count — the bookkeeping needed by validation/promotion to know a candidate generation's vector data is actually present and correct.

## Non-goals
- No multi-backend replica comparison UI/tooling yet (that's part of later backend-migration workflows, out of scope for v0.1's single-backend critical path).

## Simplicity constraints
- One `BackendReplica` record per (generation, backend name, embedding model) — do not model arbitrary N:M relationships beyond that tuple.
- Point count is tracked as a simple counter updated after each upsert batch; no live reconciliation job against the backend in this ticket (that's a `doctor` check, OPS-002).

## Design
- Uses `domain.BackendReplica` (defined in CORE-001) and the `PutBackendReplica`/`GetBackendReplica` methods already specified on `internal/control/bbolt.Store` in STORE-001:
```go
func (s *Store) PutBackendReplica(ctx context.Context, r domain.BackendReplica) error
func (s *Store) GetBackendReplica(ctx context.Context, generationID, backendID string) (domain.BackendReplica, error)
```
- Bucket: `backend_replicas`, key `backend_replicas/<generation-id>/<backend-name>`.
- Called from the generation pipeline after each `VectorBackend.Upsert` batch to increment `PointCount`; set `Status = "complete"` when all chunks are upserted, `"failed"` with `LastError` on error.

## Inputs / Outputs
- Input: generation ID, backend name, upsert batch results.
- Output: queryable `BackendReplica` record used by VAL-001 (structural validation) and OPS-001 (`status`).

## Failure behavior
- Failure to persist replica metadata after a successful backend write is logged but does not roll back the backend write (metadata is advisory bookkeeping, backend is source of truth for what's actually stored).

## Tests
- Replicating a generation's chunks updates `PointCount` incrementally and reaches `Status = "complete"`.
- A failed upsert batch sets `Status = "failed"` with a non-empty `LastError`.

## Acceptance criteria
- [ ] `GetBackendReplica` returns accurate point count and status after a full generation replication.
