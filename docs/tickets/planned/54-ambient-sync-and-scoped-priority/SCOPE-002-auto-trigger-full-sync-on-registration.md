# SCOPE-002: Auto-trigger full sync on registration

**Epic:** Ambient Sync and Scoped Priority
**Status:** planned
**Depends on:** none (independent of SCOPE-001/003/004, but SCOPE-001's progress query is what makes this actually observable rather than an invisible background surprise)
**Estimated size:** small

## Goal
Make a full, untargeted sync start automatically the first time a project is registered (`ragctl scan`), instead of requiring an operator to separately remember to run `ragctl sync --all`. Watch mode already correctly re-syncs when a manifest changes *later* — this closes the other half: the *first* sync, on registration itself.

## Non-goals
- No change to watch mode's existing change-triggered re-sync (`internal/daemon/watch.go`'s `watchLoop`) — already correct, this is additive.
- No opt-out flag in this ticket's first pass — if real usage shows a real need to disable ambient warm-up for some workflow (e.g., a CI context that only ever wants JIT), that's a follow-up once the need is real, not speculative upfront.

## Design
1. `Server.refreshProjects` (`internal/daemon/watch.go`) already diffs the current registered-project set against what the watcher already knows about (per its own "logging what started or stopped being watched" comment). Extend it: a project appearing in that diff for the *first* time (not previously known to the watcher) fires `s.scheduler.Request(id, SyncOptions{Resolve: true}, s.opts.Out)` — the exact same call `watchLoop` already makes for a later manifest change, just triggered by first-sight instead of a file event.
2. This reuses 100% of the already-built worker pool (COORD-003) and coordination (COORD-001/002) — no new sync machinery, just a new trigger point.
3. Runs in the background, at the daemon's default `Sync.MaxConcurrency` — an agent's own JIT requests (WATCH-019/020, and SCOPE-004's file-scoped priority) still preempt it to the front exactly as already proven (`TestWorkerPoolHonorsPriorityBump`).

## Inputs / Outputs
- Input: a project scanned for the first time.
- Output: a background sync request fired automatically; visible via SCOPE-001's progress query once that ships.

## Failure behavior
- If the daemon is unreachable/embedder not ready, this fails the same way any other sync attempt does today (readiness-gated, logged) — not a new failure mode.

## Tests
- A freshly-scanned project with no prior sync history gets a sync request fired automatically, without any explicit `ragctl sync` call.
- Re-scanning an already-known project does *not* fire a duplicate first-sight trigger — only genuinely new registrations do.
- A JIT request for a specific dependency, issued while the ambient full sync is still running, is provably faster than waiting for the full batch (reuse COORD-002's own cross-project regression test shape, adapted to same-project priority instead of cross-project isolation).

## Acceptance criteria
- [ ] A newly-scanned project starts syncing automatically, with no separate `ragctl sync` invocation.
- [ ] Re-registering an already-known project never fires a duplicate ambient trigger.
- [ ] An agent's JIT ask during an in-flight ambient sync is still fast, not queued behind the whole batch.
