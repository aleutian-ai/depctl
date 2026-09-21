# SCOPE-002: Auto-trigger full sync on registration

**Epic:** Ambient Sync and Scoped Priority
**Status:** done
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
- [x] A newly-scanned project starts syncing automatically, with no separate `ragctl sync` invocation.
- [x] Re-registering an already-known project never fires a duplicate ambient trigger.
- [x] An agent's JIT ask during an in-flight ambient sync is still fast, not queued behind the whole batch.

## Post-implementation notes
- `refreshProjects` fires `Request(id, SyncOptions{Resolve: true}, ...)` for a project first seen *after* the daemon's initial project-list load. The initial load only seeds the watched set: at startup every registered project is new to the watcher, and re-syncing the whole fleet on each daemon restart is not "first registration" (`watchSeeded`). `Resolve: true` is kept deliberately: a project can be visible to the refresh before its resolution is persisted, and syncing an unresolved project silently does nothing.
- **Deviation from the ticket's non-goal (no opt-out): `sync.disable_ambient` (config) / `Options.DisableAmbientSync`.** The need turned out to be real immediately, not speculative: ten existing CLI tests scan a fixture and then assert on their own sync ("dry run writes nothing", "sync already running" output), and an automatic sync behind them changed the outcomes; benchmark scripts want the same control. Default is on; the test harness's `writeTestConfig` sets it off, and tests of the ambient behavior itself run with it on.
- Third acceptance criterion (a JIT ask still jumps the queue during an ambient sync) rests on the existing `TestWorkerPoolHonorsPriorityBump` rather than a new duplicate test: an ambient run is an ordinary scheduler-launched `RunSync`, so nothing about it differs.
- **Risk worth its own ticket:** the trigger fires per project, and each project's sync has its own `MaxConcurrency` workers. Scanning a directory that registers many projects (terraform: 11) starts that many syncs at once, so total concurrency is projects x workers, not one bound. Cross-project builds of the *same* dependency coalesce, but distinct ones don't. A daemon-wide cap is the natural fix; not built.
- Projects registered before this shipped, and never synced, are not swept up — only new registrations trigger.
- Tested with a fake engine driving `refreshProjects` (startup load does not sync; a new registration syncs exactly that project, with resolve, unscoped; re-seeing known projects does not re-sync; the opt-out suppresses it). **Not verified live** against a real `ragctl scan`.
