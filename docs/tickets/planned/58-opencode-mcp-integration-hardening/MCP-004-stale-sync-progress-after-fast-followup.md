# MCP-004: `sync_progress` can report a stale/misleading result

**Epic:** OpenCode/MCP integration hardening
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
`sync_progress` must never report a result that contradicts what actually happened — specifically, it must never show `failed: 0` after a real, already-recorded failure just because a second, unrelated sync request started and finished in between.

## Problem, live-reproduced
Built a genuinely offline environment (a Podman container with `--network none` — no network interface at all) and drove the real MCP path (`scan_project` → `sync_project` → `sync_progress`) against a small real Node fixture (`chalk`, `commander`).

The daemon's own log shows the true outcome — correct, no bug here:
```
FAIL  chalk 4.1.2: embedding backend unreachable: ...
FAIL  commander 12.1.0: embedding backend unreachable: ...
0 synced, 2 failed, 0 skipped
```
But the very next `sync_progress` call returned:
```json
{"done":2,"failed":0,"total":2,"syncing":false,"note":"no sync is running; the last run finished 2 of 2 (0 failed)..."}
```
A caller reading only `sync_progress` (as an agent naturally would, per this whole epic's own found gaps) would reasonably conclude the sync succeeded. It didn't.

## Root cause, traced in the code
`Scheduler.start()` (`internal/daemon/scheduler.go`) creates a **brand-new `&SyncProgress{}`** every time a sync run begins and unconditionally assigns it to `st.progress`:
```go
func (s *Scheduler) start(projectID string, opts SyncOptions, waiters []*waiter) {
	priority := &SyncPriority{}
	progress := &SyncProgress{}
	if st := s.projects[projectID]; st != nil {
		st.priority = priority
		st.progress = progress
	}
	...
```
`SyncProgress(projectID)` always reads whatever `st.progress` currently is. `scan_project`'s automatic ambient sync (SCOPE-002) and a later explicit `sync_project` call both go through `Request()` → `start()`. Since an "embedding backend unreachable" failure resolves in milliseconds, it's entirely possible for the ambient run to finish (`st.syncing` flips back to `false`) before a caller's next request arrives — at which point that next request is a genuinely *new* run, not a coalesced follow-up, and `start()` installs a fresh, empty progress object. If that new run's own plan finds nothing further to do, its own final snapshot (`{0,0,0}`) is what's left behind — silently discarding the previous run's real, already-reported failure.

The existing code comment on `projectState.progress` ("kept after the run ends so `SyncProgress` can report the last run's outcome") already states the *intent* correctly — the bug is that intent only survives within one run's own lifecycle, not across the boundary into the next one.

## Design
Separate "the currently in-flight run's live counters" from "the last run that actually attempted real work's final outcome":
- `projectState` gains `lastMeaningful api.SyncProgress` — updated only in `finish()`, at the exact point `st.syncing` transitions back to `false` (the point a reader's "not syncing" semantics apply), and only when the just-finished run's own snapshot has `Total > 0` (it actually planned and attempted something). A run that finds nothing to do (`Total == 0`) — the common, correct case once everything is already synced — leaves the previous meaningful result in place rather than clobbering it with an empty one.
- `SyncProgress(projectID)`: while `syncing`, return the live `st.progress.Snapshot()` exactly as today (an agent polling mid-flight expects live, changing numbers, including legitimately starting at 0). Once not syncing, return `st.lastMeaningful` instead of `st.progress.Snapshot()` — the last run that actually did something, not whatever the most recently *started* run happened to leave behind.
- `start()` is unchanged — the fix is entirely in what gets exposed once a run finishes, not in how a new one begins.

## Non-goals
- No change to `syncing: true` semantics — live, in-flight progress still starts at 0 and grows normally; this ticket only fixes what's reported once a run has finished.
- No attempt to distinguish "genuinely nothing to do because everything succeeded" from "nothing to do because the last real attempt failed and nothing has retried it since" beyond what `lastMeaningful`'s own `Done`/`Failed`/`Total` already convey — a caller can already tell these apart from the numbers themselves.
- No accumulation across multiple meaningful runs (e.g. summing failures over time) — `lastMeaningful` is the *most recent* meaningful run's outcome only, matching `SyncProgress`'s existing single-run-at-a-time contract.

## Tests
- A deterministic regression test using the existing fake-`SyncFunc` scheduler test pattern (not wall-clock-dependent): run A plans and fails 2 actions; run B (a second `Request()` arriving after A finishes) plans 0 actions; `SyncProgress` after B must still report run A's real `{done:2, failed:2, total:2}`, not run B's empty `{0,0,0}`.
- A run that finds real new work after a previous failure correctly replaces `lastMeaningful` with its own outcome (no permanent staleness — `lastMeaningful` does update, just never to an empty/no-op result).
- The live offline reproduction above, re-run after the fix, as a real end-to-end confirmation.

## Acceptance criteria
- [x] `sync_progress`, when not syncing, reports the last run that actually attempted work — never a more recent no-op's empty counters papering over a real prior failure.
- [x] `syncing: true` behavior is unchanged.
- [x] The live offline repro (`chalk`/`commander`, `--network none`) shows `sync_progress` correctly reporting `failed: 2` after the real failure.

## Post-implementation note

Shipped as **two** fixes, not one — the first attempt was real but incomplete, caught by continuing to live-verify rather than stopping at the first passing test.

**Fix 1 — `projectState.lastMeaningful`** (`internal/daemon/scheduler.go`), exactly as designed: captured in `finish()`, read by `SyncProgress()` once not syncing. First version placed the capture *after* the dirty-coalescing branch's early return, so a coalesced follow-up (a request arriving while the previous run was still executing — the common real shape, not the sequential-after-idle shape the first regression test happened to cover) skipped the capture entirely. Moved the capture to run unconditionally before that branch. `TestSyncProgressSurvivesADirtyCoalescedFollowUp` is the direct regression for this exact ordering, deliberately distinct from `TestSyncProgressSurvivesAFastNoOpFollowUp` (which only proves the sequential case).

**Fix 2 — the real root cause, found by not trusting a live repro that still showed the bug after fix 1 was live-verified working in isolation.** Debug-traced the actual live run and found the second sync attempt wasn't a true no-op at all — the planner correctly produced two `NOOP` actions (`internal/planner`'s "nothing changed, nothing to plan" outcome), but `runSyncAction`'s `defer progress.Finish(name, failed > 0)` fired unconditionally for *every* action kind, including `NOOP`, `ADD_REFERENCE`, `DROP_REFERENCE`, and `GC_CANDIDATE` — none of which represent "syncing a dependency's content," the thing `Total`/`Done`/`Failed` exist to describe. A `NOOP` action was silently counted as "done, not failed," indistinguishable from a real success — this, not fix 1's own scope, was the actual mechanism producing the original bug report's `{"done":2,"failed":0}` after a real 2-failure sync. Fixed by scoping both `SetTotal` (`internal/cli/sync.go`, now counts only `ActionSyncVersion` actions) and `runSyncAction`'s `progress.Finish` call (now only fires for `ActionSyncVersion`, matching `progress.Begin`'s own pre-existing scoping) consistently. `TestRunSyncNoopActionsDoNotCountAsProgress` is the direct regression, using the exact same no-op fixture shape as PLAN-003's own established `TestSyncNoOpPlanMakesNoNetworkCalls`.

With fix 2 in place, fix 1's own `Total > 0` guard becomes exactly correct (a genuine no-op now really does produce `Total == 0`), rather than being a heuristic papering over actions that happened to exist but conveyed nothing.

Verified live, end to end, in a genuinely offline environment (Podman with `--network none`) against two real npm packages (`chalk`, `commander`): `sync_progress` now correctly reports `{"done":2,"failed":2,"total":2}` after the real failure, and continues to report it correctly after a follow-up `sync_project` call. Full suite green (build/vet/test, `-race`), five new tests total (three scheduler-level, two `internal/cli`-level).
