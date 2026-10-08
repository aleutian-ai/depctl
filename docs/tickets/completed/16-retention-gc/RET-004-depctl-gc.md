# RET-004: `depctl gc`

**Epic:** Retention and GC
**Status:** done
**Depends on:** RET-003, VEC-002, STORE-003
**Estimated size:** medium

## Goal
Implement the `depctl gc` command: run RET-003's planner, then delete GC-eligible version data across vector backend, Badger, and bbolt, in a restartable, idempotent way.

## Non-goals
- Interactive confirmation UI beyond a simple `--dry-run` flag and printed plan.
- Parallel GC across multiple backends simultaneously (v0.1: one configured backend).

## Simplicity constraints
- Reuse the existing job system (bbolt `jobs` bucket, SyncJob/JobState from CORE-002) instead of inventing a separate GC-job type; a GC run is just jobs of type `gc`.
- Deletion order is fixed and sequential — no attempt at cross-store transactional deletion (that's what restartability via job state is for).

## Design
Package: `internal/lifecycle/gc` + `cmd/depctl` `gc` command (Cobra).

```bash
depctl gc [--dry-run]
```

Flow per `GCCandidate`:
```text
1. create/claim a Job{Type: "gc", Dependency, Version}
2. delete vector replica (VectorBackend.Delete by generation/version filter)
3. delete Badger generation/chunk objects (DeleteGeneration from STORE-003)
4. delete bbolt generation + dependency_version + reference metadata
5. mark Job SUCCEEDED
```

If interrupted mid-run, re-invoking `depctl gc` re-claims any `PENDING`/`RETRY` gc jobs and resumes from step 2 for that candidate (each step is itself idempotent — deleting something already gone is a no-op, not an error).

`--dry-run` runs RET-003's `PlanGC` and prints candidates without creating jobs or deleting anything.

## Inputs / Outputs
- Input: none (reads store state).
- Output: human-readable report of what was deleted (or would be deleted, for `--dry-run`); job records in bbolt.

## Failure behavior
- A single candidate's deletion failure marks its job `FAILED` with `LastError` and does not block other candidates.
- Backend unavailable during step 2 → job goes to `RETRY` with backoff; `depctl gc` exits non-zero but has made partial progress safely.

## Tests
- `--dry-run` lists candidates without deleting anything (store state unchanged).
- Full GC run deletes vector replica, Badger objects, and bbolt metadata in order.
- Simulated crash after step 2 (vector deleted, Badger/bbolt not yet) — re-running `depctl gc` completes steps 3-4 without error.
- Orphaned/unreferenced version removed end-to-end; referenced version untouched.

## Acceptance criteria
- [x] Deletion order: vector replica → Badger → bbolt.
- [x] GC is restartable and idempotent (re-run after partial failure completes cleanly).
- [x] `--dry-run` performs no mutations.

## Post-implementation note
`SyncJob`/`Job` (CORE-002's own design) had never actually been built — only the `JobState` enum existed (`internal/domain/lifecycle.go`), the `jobs` bbolt bucket was provisioned but unused, and CORE-001's `SyncJob` was still just a comment pointing at CORE-002. Built the minimum this ticket needs: `domain.Job` (ID, Type, Dependency, State, LastError, timestamps) plus `Store.PutJob`/`GetJob` — no generic scheduler, no job-type registry, matching the ticket's own "reuse the existing job system... a GC run is just jobs of type `gc`" instruction as literally as possible given "the existing job system" was mostly not there yet. Job IDs are deterministic (`gc.JobID`, BLAKE3 over type+dependency+version, not a ULID) specifically so re-running `depctl gc` against the same candidate finds and resumes the same job rather than creating a duplicate — the restartability this ticket requires.

Two bbolt primitives RET-004's design assumed but didn't name a method for were added: `ListGenerationsByDependencyVersion` (full `generations` bucket scan — no index from dependency+version to generation ID exists, same "small keyspace" tradeoff as RET-001/003) and `DeleteAllReferences` (RET-001's `RemoveReference` only removes one project's reference; step 4 needs every remaining reference — including the `grace_period` one — gone).

Verified end-to-end against real bbolt, real Badger, and `backendtest.Backend` (not a live Qdrant — `internal/backend/qdrant` itself is already proven separately against a real container in epic 13): `TestGCEndToEndRemovesOrphanedVersionLeavesReferencedVersionUntouched` builds and replicates two real generations of the same dependency via `generation.Build`/`Replicate`, drops and grace-expires one, runs `PlanGC` + `Run`, and confirms the orphaned version's data is gone from all three stores while the referenced version's chunks/points/records are untouched — plus a re-run proving idempotency. `TestRunResumesAfterPartialFailure` and `TestRunFailureMarksJobFailedAndDoesNotBlockOtherCandidates` use a hand-written fake `ControlStore`/`DataStore` to simulate the specific interruption points (Badger-delete failure, list failure) the ticket's Tests section describes, without needing to actually break a real store mid-operation.
