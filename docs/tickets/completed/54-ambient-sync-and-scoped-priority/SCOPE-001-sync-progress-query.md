# SCOPE-001: Sync progress query

**Epic:** Ambient Sync and Scoped Priority
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
Let any caller — `depctl status`, a new MCP resource/tool, a separate session checking in later — ask "how far along is project X's in-flight sync" (done/total, current dependency) without holding open the call that started it, or waiting out another bounded window for a fresh snapshot.

## Non-goals
- No change to `sync_project`'s own bounded-wait/`still_running` contract (already correct, see its own doc comment and `mcpSyncWaitBound`) — this adds a way to check progress *between* calls, not a replacement for it.
- No historical progress log (past syncs, timing history) — live, current-state only.

## Simplicity constraints
- Reuse what already tracks this shape internally: `Scheduler.projects[id].syncing`/`dirty` already exists; the missing piece is a numeric done/total counter, not a new state machine.

## Design (amended after measuring)
STRESS-005's phase timings showed 97.8% of a sync's time is in one phase (replicate: embed + upsert), and a single large dependency can spend 10 minutes there — so a dependencies-done counter alone would sit still for minutes and look hung. The progress therefore has two levels: actions done/failed/total, **and** each in-flight dependency's chunks embedded/total. The chunk numbers come from `Replicate` itself, which reports after every batch through a context-carried callback (`generation.WithProgress`); nothing new is stored. Deliberately no ETA: the p90 dependency takes ~4x the median, so any estimate would be wrong most of the time.

Original design:
1. Add a small progress counter to the per-project state the scheduler already tracks (`projectState`, `internal/daemon/scheduler.go`) — `done, total int`, updated as `RunSync`'s workers complete actions (reuse the same `writeLine`/counter-increment path already wired for `synced`/`failed`/`skipped`, just also updating a value the scheduler can read back).
2. New `Scheduler.SyncProgress(projectID string) (done, total int, syncing bool)` — a cheap, lock-protected read, no side effects.
3. Expose it two ways:
   - `depctl status`'s existing `Status` struct (`internal/daemon/api/api.go`) gains it per-project when a sync is in flight — a human just runs `depctl status` again.
   - A new MCP resource (not a tool — this is read-only, no side effects, the right shape for cheap polling) reporting the same for a given project.

## Inputs / Outputs
- Input: a project ID.
- Output: done/total/syncing — `syncing: false` with the last-known done/total (or zeros if never synced) when nothing is currently running.

## Failure behavior
- A project ID that's never been synced reports `syncing: false, done: 0, total: 0` — not an error.

## Tests
- Progress counter increments correctly under the real worker pool (reuse COORD-003's own concurrent-worker test fixtures) — `done` reflects actual completions, not queue position.
- `SyncProgress` for a project with nothing running reports `syncing: false` promptly (no stale "still syncing" after a run actually finishes).
- The MCP resource and `depctl status` report the same numbers for the same in-flight sync (no drift between the two surfaces).

## Acceptance criteria
- [x] `depctl status` shows live done/total for any project with an in-flight sync.
- [x] A new MCP tool (`sync_progress` — see the notes for why not a resource) reports the same, queryable independent of whatever call originally triggered the sync.
- [x] Progress numbers are accurate under real concurrent workers, not just the old sequential loop.

## Post-implementation notes
- `daemon.SyncProgress` (nil-receiver-safe, like `SyncPriority`) is created per run by `Scheduler.start` and kept afterwards so `Scheduler.SyncProgress(id)` reports the last run once it ends (`syncing: false`, never a stale "still syncing"). `SyncingProjects()` lists every in-flight run.
- Surfaces: `depctl status` (a `syncs:` block naming each project by root with its in-flight dependencies), the daemon endpoint `POST /v1/sync/progress`, and the MCP tool `sync_progress`.
- **Deviation from the ticket: a tool, not an MCP resource.** No resource is registered anywhere in this server today, and I have not verified that opencode (the client the bounded-wait calibration was measured against) reads MCP resources at all; agents reliably call tools. `sync_project`'s still-running note now points at it. If a resource is wanted too, it can wrap the same `SyncProgressReader`.
- Counters count planned *actions* (nearly one per dependency version), including reference add/drop actions.
- Tested: tracker (incl. concurrent workers under `-race`), scheduler live-then-last-run, the real worker pool with real `Replicate` (an in-flight snapshot taken mid-embed had a chunk total), the status overlay and text rendering, the endpoint, and the tool over a real in-memory MCP client/server. **Not verified live** against a real daemon and a real long sync — the verification run in progress predates this code.
