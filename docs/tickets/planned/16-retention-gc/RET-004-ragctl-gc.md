# RET-004: `ragctl gc`

**Epic:** Retention and GC
**Status:** planned
**Depends on:** RET-003, VEC-002, STORE-003
**Estimated size:** medium

## Goal
Implement the `ragctl gc` command: run RET-003's planner, then delete GC-eligible version data across vector backend, Badger, and bbolt, in a restartable, idempotent way.

## Non-goals
- Interactive confirmation UI beyond a simple `--dry-run` flag and printed plan.
- Parallel GC across multiple backends simultaneously (v0.1: one configured backend).

## Simplicity constraints
- Reuse the existing job system (bbolt `jobs` bucket, SyncJob/JobState from CORE-002) instead of inventing a separate GC-job type; a GC run is just jobs of type `gc`.
- Deletion order is fixed and sequential — no attempt at cross-store transactional deletion (that's what restartability via job state is for).

## Design
Package: `internal/lifecycle/gc` + `cmd/ragctl` `gc` command (Cobra).

```bash
ragctl gc [--dry-run]
```

Flow per `GCCandidate`:
```text
1. create/claim a Job{Type: "gc", Dependency, Version}
2. delete vector replica (VectorBackend.Delete by generation/version filter)
3. delete Badger generation/chunk objects (DeleteGeneration from STORE-003)
4. delete bbolt generation + dependency_version + reference metadata
5. mark Job SUCCEEDED
```

If interrupted mid-run, re-invoking `ragctl gc` re-claims any `PENDING`/`RETRY` gc jobs and resumes from step 2 for that candidate (each step is itself idempotent — deleting something already gone is a no-op, not an error).

`--dry-run` runs RET-003's `PlanGC` and prints candidates without creating jobs or deleting anything.

## Inputs / Outputs
- Input: none (reads store state).
- Output: human-readable report of what was deleted (or would be deleted, for `--dry-run`); job records in bbolt.

## Failure behavior
- A single candidate's deletion failure marks its job `FAILED` with `LastError` and does not block other candidates.
- Backend unavailable during step 2 → job goes to `RETRY` with backoff; `ragctl gc` exits non-zero but has made partial progress safely.

## Tests
- `--dry-run` lists candidates without deleting anything (store state unchanged).
- Full GC run deletes vector replica, Badger objects, and bbolt metadata in order.
- Simulated crash after step 2 (vector deleted, Badger/bbolt not yet) — re-running `ragctl gc` completes steps 3-4 without error.
- Orphaned/unreferenced version removed end-to-end; referenced version untouched.

## Acceptance criteria
- [ ] Deletion order: vector replica → Badger → bbolt.
- [ ] GC is restartable and idempotent (re-run after partial failure completes cleanly).
- [ ] `--dry-run` performs no mutations.
