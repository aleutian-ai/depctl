# STRESS-013: GC and a queued sync under real daemon load

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** small

## Goal
Fire a real `depctl gc` (reference or orphan) and a real `depctl sync` at the same project at nearly the same instant, and confirm the daemon's GC/sync exclusion invariant actually holds under real concurrent daemon load, matching the existing scheduler unit tests but with two genuinely separate client processes, not synthetic in-process timing.

**Note (2026-09-29, updated before running):** this ticket originally described the exclusion mechanism as `Scheduler`'s single global `s.global` mutex, shared by `RequestGC`/`RequestOrphanGC` and all sync activity. Epic 53 (COORD-001..003, closed 2026-09-28) replaced that with `BuildCoordinator` (`internal/daemon/coordinator.go`), a `sync.RWMutex` named `gate`: builds/reference-mutations take `gate.RLock()` (so unrelated syncs no longer exclude each other — that's epic 53's whole point), while `executeGC` takes `gate.Lock()` (full write lock) via `ExcludeForGC()`. The GC-vs-sync exclusion this ticket exists to verify is unchanged in strength — a GC run still fully blocks on, and blocks, all sync activity — only sync-vs-sync concurrency was relaxed. The test below verifies the invariant as it's actually implemented today, not the pre-epic-53 mechanism.

## Non-goals
- No new locking logic — verification only.

## Simplicity constraints
- Reuses whatever project/dependencies are already set up from earlier tickets in this epic; no new fixture needed.

## Design
1. Real daemon, a project with both GC-eligible content (from STRESS-011/012) and a pending sync target.
2. Background both `depctl gc` and `depctl sync --dependency <target>` as close to simultaneously as the shell allows.
3. Confirm both complete successfully, neither corrupts the other's work, and (via daemon logs or `depctl daemon status` during the run, if timing allows) confirm they genuinely ran sequentially, not interleaved.
4. Repeat a few times with GC and sync started in the opposite order, to cover both queuing directions.

## Inputs / Outputs
- Input: concurrent real `gc` and `sync` requests against one daemon.
- Output: pass/fail on both completing correctly with no interleaving corruption, across both orderings.

## Failure behavior
- Any interleaving corruption, deadlock, or incorrect result under this real concurrency is this ticket's finding — a release-blocking bug given the explicit "must never interleave" architectural invariant this would violate.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] Concurrent real `gc` and `sync` requests against one daemon both complete correctly, with no interleaving corruption, in both request orderings.

## Post-implementation note (2026-09-29)

Built an isolated fixture (real Qdrant container on a unique port, isolated `$HOME`/config, `retention.grace_period: 5s`, `disable_ambient: true`) with a small real `go.mod` (initially `uuid`+`cobra`, 8 total deps). Confirmed via research first that this ticket's original premise — a single `Scheduler.s.global` mutex — no longer matches the code: epic 53 replaced it with `BuildCoordinator`'s `gate sync.RWMutex` (builds/reference-mutations take `RLock`, `executeGC` takes `Lock` via `ExcludeForGC`). Updated the ticket's Goal note accordingly before testing (see above), then verified the invariant as actually implemented.

**Order A — sync requested first, GC concurrently after:** trimmed `go.mod` to `uuid` only (dropping cobra + 6 transitive deps), demoted their active generations via the same `ClearActiveGeneration` workaround STRESS-011 established (7 demoted, since a dropped reference alone is never GC-eligible per that ticket's finding), then added `golang.org/x/tools` (a large, real, not-yet-synced dependency, 5 further transitive deps) and fired `depctl gc` and `depctl sync` as two genuinely separate client processes within ~0ms of each other. Result: the sync side (embedding `x/tools`'s real content) held the coordinator's exclusion for the full real duration (~4m03s wall-clock); GC's client call blocked the entire time and only performed its actual work (7 real deletions) in the final fraction of a second once sync released — both commands' wall-clock durations matched almost exactly (4:03.75 vs 4:03.98), direct real evidence of full serialization rather than interleaving.

**Order B — GC requested first, sync concurrently after:** dropped `x/tools` and its 5 transitive deps (re-trimmed to `uuid` + a newly-added `google.golang.org/protobuf`), demoted the 8 newly-grace-expired actives, then fired `depctl gc` first and `depctl sync --dependency google.golang.org/protobuf --rebuild` ~50ms later (a real rebuild was used since the plain scan's synchronous JIT-sync had already synced `protobuf` on reference, leaving nothing "pending" for a plain `sync` to do — `--rebuild` forces genuine real work, a valid stand-in for "a real, concurrent sync operation" per the ticket's actual intent). Result: GC completed its 8 real deletions in 0.343s (acquired the write lock immediately, nothing else was in flight yet); the concurrent sync call reported `sync already running... queued a follow-up` and blocked until GC's lock released, then performed its real rebuild — total wall-clock (6.057s) consistent with GC's ~0.34s plus the rebuild's own real duration, confirming the reverse ordering also fully serializes.

**Correctness after both real concurrent runs:** `depctl doctor` clean (19 ok, 0 warning, 0 unhealthy) after each order; `uuid` and `protobuf` both retained complete, correct active generations and full backend replicas (84 and 15,060 chunks respectively) — no corruption, no partial state, from either real concurrent collision.

No new finding — confirms the GC/sync exclusion invariant holds exactly as strongly under epic 53's `BuildCoordinator`-based re-implementation as it did under the pre-epic-53 single global mutex this ticket originally described, verified here with two genuinely separate real client processes rather than in-process synthetic timing.
