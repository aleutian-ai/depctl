# VEC-003: Backend replica metadata

**Epic:** Vector Backend (Qdrant)
**Status:** done
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
- [x] `GetBackendReplica` returns accurate point count and status after a full generation replication.

## Post-implementation note
No ticket in the plan explicitly specified the driver that wires embedding + `generation.Build`'s staged chunks + `VectorBackend.Upsert` together — GEN-002 scoped embedding/replication out, and VEC-003's own Design section assumed that driver already existed ("called from the generation pipeline after each `Upsert` batch"). Built it as `generation.Replicate` (`internal/data/generation/replicate.go`), alongside `Build`, since it's the same lifecycle-advancing shape: `Replicate(ctx, gen, sources, embedder, vb, ns, store, badgerStore)` lists a generation's staged chunks, resolves each chunk's parent `KnowledgeObject` (cached per call — many chunks share one object), embeds in batches of 64, upserts into the vector backend, and persists `BackendReplica` progress after each batch so a crash loses at most one batch's bookkeeping. Any embedding or upsert failure marks the replica `failed` with the error recorded and returns immediately — verified end-to-end (`TestReplicateEmbedsAndUpsertsAllChunks` runs a real `Build` against a git fixture, then `Replicate`s it through a fake embedder into `backendtest.Backend` and confirms both the replica bookkeeping and the actual stored point metadata; `TestReplicateFailureMarksReplicaFailedWithError` covers the failure path; `TestReplicateWithNoChunksCompletesWithZeroPoints` covers the empty case) rather than simulated.

**Correctness fix during epic 14 (two rounds):** the first version of `Replicate` stamped each point's ecosystem/dependency/version metadata from the chunk's parent `KnowledgeObject.Dependency` field. That's wrong for a reused object (GEN-003): after content reuse, `obj.Dependency` reflects whichever generation *first* created that object, not every generation that now references it via a shared chunk. A chunk reused unchanged across a version bump would silently carry the *old* version's metadata into the *new* generation's vector points — exactly the kind of cross-version contamination VAL-003 exists to catch. Fixed by stamping ecosystem/dependency/version/generation from `gen` (the generation actually being replicated) instead. Caught by reasoning through VAL-001's design while building it, confirmed with `TestReplicatePointsUseGenerationVersionNotStaleObjectVersion`, and independently exercised by VAL-003's own `TestVersionCorrectnessIsolatesTwoIndexedVersions`.

At the time, `SourceType`/`Authority` were left sourced from the object on the assumption those "genuinely don't change across reuse." An independent adversarial review of this epic caught that this assumption is also wrong: `Authority` is user/registry-configurable (`internal/registry`'s `Loader` supports user/project overrides — see REG-002), so a user can bump a source's authority in config and re-sync a dependency whose content didn't change, and the reused object's stale `Authority` would leak into the new generation's points exactly like `Version` did. Fixed the same way: `Replicate` now takes a `sources []registry.Source` parameter (the dependency's current registry match) and stamps `SourceType`/`Authority` from the current source config, falling back to the object's own stored fields only if that source ID is no longer present in `sources`. Regression test: `TestReplicatePointsUseCurrentSourceAuthorityNotStaleObjectAuthority` (re-syncs an unchanged dependency with a bumped authority value, confirms every point reflects the new value, not the one baked into the reused object).
