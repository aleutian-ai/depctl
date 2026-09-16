# WATCH-020: Let an urgent single-dependency ask jump an in-progress background sync's queue

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-019 (JIT sync-on-search — this ticket only changes behavior for the case WATCH-019 doesn't cover: a sync is *already running* for the project)
**Estimated size:** medium

## Goal
`internal/daemon/scheduler.go`'s `global sync.Mutex` means the whole daemon runs one sync at a time — not just one per project. So if a background "warm the rest of the project" sync (WATCH-018's `still_running` case, or a plain `sync_project` with no dependency filter) is already in flight when the agent asks about a *different* dependency (WATCH-019's JIT-sync case), WATCH-019's own mechanism would just queue a second request behind the first, global-lock-serialized — defeating the entire point. Let the already-running sync reorder its own remaining work instead: the requested dependency jumps to the front of the queue, the rest keeps going in the background, and the caller waits only for that one dependency rather than for the whole run ahead of it.

## Non-goals
- No true preemption — nothing gets killed or interrupted mid-flight. `RunSync`'s existing per-dependency action loop already treats one `SYNC_VERSION` action as an atomic, already-fast unit; reordering the *remaining* queue between actions is sufficient and far simpler/safer than aborting an in-progress embed/write.
- No concurrent syncs — `global sync.Mutex` stays exactly as-is. This ticket is entirely about reordering work *within* one already-running, lock-holding `RunSync` call, never about running two at once.
- No change to `sync_project`'s own behavior when *no* sync is currently running for the project — that's WATCH-019's plain JIT-sync path, unchanged by this ticket.
- No generalized priority/scheduling system beyond "one dependency name can jump the queue" — no priority levels, no multiple simultaneous bumps competing with each other beyond simple arrival order (a small FIFO of pending bumps is enough).

## Simplicity constraints
- One new small type, `syncPriority` (`internal/daemon/scheduler.go` or a new `priority.go` in the same package): a mutex-guarded FIFO of dependency names, `bump(dep string)` (append) and `drain() []string` (pop everything, clear) — no channels, no goroutines of its own.
- `projectState` (`scheduler.go:112`) gains one field, `priority *syncPriority`, created when a run starts and left in place for the run's duration — not per-request, since the whole point is one live object a concurrent caller can reach while the run is executing.
- `RunSync` (`internal/cli/sync.go`) gains a way to consult it between actions — passed through `SyncOptions` (already the one struct threaded uniformly from `Scheduler.Request` through `engine.Sync` to `RunSync`), not a new parameter added to every function in the call chain.

## Design
`internal/daemon/scheduler.go`:
```go
// syncPriority is a small FIFO of dependency names a concurrent caller
// wants prioritized within an already-running sync — see WATCH-020.
type syncPriority struct {
	mu      sync.Mutex
	pending []string
}

func (p *syncPriority) bump(dependency string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pending = append(p.pending, dependency)
}

// drain returns every pending bump and clears it, for RunSync to
// consult between actions — a non-blocking, cheap poll, not a channel
// read (RunSync's loop already runs synchronously; there's no natural
// place to block on a channel between actions).
func (p *syncPriority) drain() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	pending := p.pending
	p.pending = nil
	return pending
}
```
`SyncOptions` (`scheduler.go:29`) gains a `Priority *syncPriority` field, set by `Scheduler.start` when a run begins (`execute`, `scheduler.go:307`, reads it from the same `projectState` `Request` already looked up) and left nil for anything constructed directly (CLI's `ragctl sync`, tests) — nil is the "no live priority queue, behave exactly as today" case throughout, so this is purely additive.

`Scheduler` gains:
```go
// BumpPriority asks the currently-running sync for projectID (if any)
// to prioritize dependency next. Returns false if no sync is currently
// running for projectID — the caller (WATCH-019's JIT-sync path) falls
// back to its own plain Request in that case, unchanged.
func (s *Scheduler) BumpPriority(projectID, dependency string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.projects[projectID]
	if st == nil || !st.syncing || st.priority == nil {
		return false
	}
	st.priority.bump(dependency)
	return true
}
```

`internal/cli/sync.go`'s `RunSync` gains a `priority *syncPriority` parameter (nil-safe, mirroring `embeddingReadiness`/`vectorReadiness`'s own nil-receiver pattern already established there). Its action loop — today a straightforward nested range over `plans`/`pp.Actions` — is restructured to pull from a queue it can reorder:
```go
queue := flattenSyncVersionActions(plans) // []planner.Action, SYNC_VERSION only, same order as today
for len(queue) > 0 {
	if priority != nil {
		for _, dep := range priority.drain() {
			queue = bumpToFront(queue, dep)
		}
	}
	action := queue[0]
	queue = queue[1:]
	// ... existing syncVersion/getPipeline call, unchanged
}
```
Non-`SYNC_VERSION` actions (`ADD_REFERENCE`/`DROP_REFERENCE`) are unaffected — they're not what WATCH-019's JIT-sync is ever waiting on, and reordering them has no user-visible effect worth the complexity; they keep running in their existing plan order, interleaved before/after the reorderable `SYNC_VERSION` queue exactly as today's single loop already produces (a small refactor of the loop body, not a change to what gets synced or in what overall phase order relative to reference bookkeeping).

**WATCH-019's own handler** (`internal/mcp/tools.go`'s JIT-sync branch) changes to try the fast path first:
```go
if bumped := schedulerBumpPriority(in.ProjectID, in.Dependency); bumped {
	// A background sync is already running for this project — wait for
	// just this dependency instead of queuing a second, fully
	// redundant, lock-serialized run behind it.
	waitForActiveGeneration(ctx, ...) // bounded poll, WATCH-018-style timeout
} else {
	sync.SyncProject(ctx, in.ProjectID, in.Dependency, nil) // WATCH-019's plain path, unchanged
}
```
This needs `SyncTrigger` (the MCP-consumer-side interface) to expose a `BumpPriority`-shaped capability too — either a new method on `SyncTrigger` itself, or a second small interface (`PriorityBumper`?) `Deps` carries alongside `Sync`. Left as an implementation decision: whichever keeps `internal/mcp`'s consumer-defined-interface convention (ADR/architecture's existing pattern — interfaces defined where they're used, not where they're implemented) cleanest once the exact polling mechanism for "wait until this one dependency is active" is settled — see Failure behavior below for why that polling piece is the part most worth getting right, not the interface shape around it.

## Inputs / Outputs
- Input: a dependency name to prioritize, and whether a sync is currently running for that project.
- Output: the prioritized dependency's `SYNC_VERSION` action runs next (after whatever's already mid-flight, which always finishes first — no interruption), while the rest of the background run continues afterward in its original order.

## Failure behavior
- No sync currently running for the project: `BumpPriority` returns `false`, caller falls back to WATCH-019's plain path — behavior identical to a world without this ticket.
- The bumped dependency isn't actually in the running sync's plan at all (a stale/mismatched request, or the plan already finished it before the bump was drained): the wait-for-active-generation poll (mirroring WATCH-018's bound) times out with the same actionable "still working, check back" shape WATCH-018 already established, not a hang.
- Two bumps for different dependencies arrive close together: both get processed, FIFO order — the second doesn't starve the first, and neither is silently dropped.

## Tests
- A fake `SyncFunc` that blocks on a channel between two dependencies, with a `syncPriority` injected: `BumpPriority` called mid-run moves the third-in-plan dependency ahead of the second, verified via the order `syncVersion` (or a fake standing in for it) is actually invoked.
- `BumpPriority` against a project with no in-flight sync returns `false` and has no effect on a subsequent, fresh `Request`.
- `RunSync` with a `nil` priority behaves byte-for-byte identically to today (regression guard — this must never be a behavior change for the CLI's own `ragctl sync`, which never sets one).
- End-to-end (real daemon): a background whole-project sync kicked off, then a `search_dependency_docs` call for a not-yet-synced dependency later in the plan resolves meaningfully faster than the background run's own natural arrival order would have produced — measured, not just asserted structurally.

## Acceptance criteria
- [x] A dependency requested via WATCH-019's JIT-sync path while a background sync is already running for the same project gets prioritized within that run, rather than queued behind it.
- [x] Nothing is interrupted mid-flight — the currently-executing action always finishes normally before any reordering takes effect.
- [x] `ragctl sync` (CLI) and any caller that doesn't set a priority queue are entirely unaffected — `nil` is a true no-op path.
- [x] A bump for a dependency not actually in the running plan (or arriving after that dependency already finished) fails cleanly with an actionable timeout, not a hang.

## Post-implementation note
Shipped complete, including the caller-side wiring the Design section had explicitly left as an open decision. Resolved it with a new daemon HTTP endpoint (`POST /v1/sync/priority`, `api.SyncPriorityRequest`/`Response`) — needed because `Scheduler.BumpPriority` lives inside the daemon process, and `internal/mcp` runs inside the separate `ragctl serve` process (ADR-011), reachable only over the Unix socket like everything else. New consumer-side `mcp.PriorityBumper` interface (`BumpSyncPriority(ctx, projectID, dependency) (bool, error)`), implemented by `daemonPriorityBumper` (`internal/cli/query_client.go`), wired into `Deps.Priority` in `serve.go`.

`searchDependencyDocsHandler` now tries `PriorityBumper.BumpSyncPriority` first on a missing generation; a `true` result means a background sync is already running, so it polls (`waitForDependencyGeneration`, bounded by `jitSyncPriorityWaitBound` — 90s, matching `mcpSyncWaitBound`'s WATCH-018 calibration) instead of ever calling `SyncTrigger.SyncProject` a second time. A `false` result (the common case — nothing running) falls straight through to WATCH-019's existing plain JIT-sync path, unchanged.

`SyncOptions` gained `Priority *SyncPriority`; `Scheduler.start` creates a fresh one for every launched run (including follow-ups) and clears it in `finish` once a project goes fully idle. `RunSync`'s action loop was restructured from a static nested range into a flattened, poppable queue (`bumpActionToFront`) — verified to reproduce today's exact traversal order byte-for-byte when no bump ever arrives (the regression guard the ticket called for), and to move a bumped dependency's `SYNC_VERSION` action ahead of *any* other remaining action kind, not just other syncs, matching "prioritize means it runs next."

One pre-existing test (`TestSchedulerMergesFollowUpOptions`, `internal/daemon/scheduler_test.go`) needed updating: it asserted `SyncOptions` equality against a literal with a nil `Priority`, which is no longer possible now that every scheduler-launched run legitimately gets a non-nil one — fixed by asserting `Priority != nil` separately, then comparing the rest with it cleared.

Full suite green, including dedicated coverage at every layer: `bumpActionToFront` (pure unit tests), `Scheduler.BumpPriority` (reaches the live running run's actual `SyncPriority` object, returns false with nothing running or after a run finished), `daemonPriorityBumper` (real daemon, real HTTP round trip), and `searchDependencyDocsHandler` (prefers the bump path over a redundant sync, falls back correctly when nothing's running, times out cleanly rather than hanging when a bump never resolves).
