# FIX-001: Replicate failure transitions Generation.State to FAILED

**Epic:** Generation Lifecycle Consistency
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
Recommended by VALID-001's post-implementation note: a `generation.Replicate` failure only marked the *`BackendReplica`* record `FAILED` (`domain.BackendReplica.Status`) and returned, leaving the `Generation` record itself stuck at whatever state `Build` last set it to (`INDEXING`) — unlike a `Build`-stage failure, which already calls `generation.fail` explicitly and produces a correctly-`FAILED` `Generation` record. Functionally harmless for query correctness (an `INDEXING`-state generation was never promoted or served either way, confirmed by VALID-001's own test before this fix), but a real observability gap: `ragctl status`/`doctor`, and GC-001's own orphan-candidate discovery (`retention.PlanOrphanGC`), both inspect `Generation.State` directly and would see a generation stuck mid-build, not visibly failed.

## Non-goals
- No change to `BackendReplica`'s own failure handling — it already correctly marks itself `FAILED`; this ticket only adds the missing `Generation`-level transition alongside it.
- No retry/self-healing behavior — a `FAILED` generation is retried by a normal future `sync`, exactly as a `Build`-stage failure already is.

## Simplicity constraints
- One-line addition to `internal/data/generation/replicate.go`'s existing `failReplica` helper — reuses `fail` (`generation.go`), the exact function `Build`'s own failure path already calls, rather than duplicating its logic.

## Design
`failReplica` gains a `gen *domain.Generation` parameter and calls `fail(ctx, store, gen, err.Error())` alongside its existing `BackendReplica`-marking logic. All five call sites in `Replicate` pass `&gen`.

## Inputs / Outputs
- Input: any failure during `Replicate` (namespace setup, chunk listing, embedding, upsert, progress persistence).
- Output: both `BackendReplica.Status == "failed"` and `Generation.State == GenFailed` (with `Generation.Error` set), consistently.

## Failure behavior
No change to `Replicate`'s own error return — `failReplica` still returns the original error unchanged; the new `fail` call is best-effort persistence (matching `fail`'s own existing behavior: a failure to persist the FAILED state is logged, not returned).

## Tests
- `TestReplicateFailureMarksReplicaFailedWithError` (`internal/data/generation/replicate_test.go`) extended to also assert `Generation.State == GenFailed` and `Generation.Error != ""`, alongside its existing `BackendReplica` assertions.
- `internal/cli/sync_atomic_promotion_test.go`'s `TestSyncVersionAtomicPromotionUnderReplicateFailure` (VALID-001) updated from documenting the old INDEXING-stuck finding to asserting `Generation.State == GenFailed` directly, now that both Build-stage and Replicate-stage failures produce the same, consistent result.

## Acceptance criteria
- [x] A `Replicate`-stage failure produces `Generation.State == FAILED`, matching a `Build`-stage failure.
- [x] `Generation.Error` is set to the same failure message the `BackendReplica` record already carries.
- [x] `go test ./internal/data/generation/...` and `./internal/cli/...` pass with the updated assertions.

## Post-implementation note
Implemented exactly as scoped — no surprises, no reverts. Full repo build/vet/gofmt/test clean.
