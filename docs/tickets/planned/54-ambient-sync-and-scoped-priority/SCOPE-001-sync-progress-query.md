# SCOPE-001: Sync progress query

**Epic:** Ambient Sync and Scoped Priority
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
Let any caller — `ragctl status`, a new MCP resource/tool, a separate session checking in later — ask "how far along is project X's in-flight sync" (done/total, current dependency) without holding open the call that started it, or waiting out another bounded window for a fresh snapshot.

## Non-goals
- No change to `sync_project`'s own bounded-wait/`still_running` contract (already correct, see its own doc comment and `mcpSyncWaitBound`) — this adds a way to check progress *between* calls, not a replacement for it.
- No historical progress log (past syncs, timing history) — live, current-state only.

## Simplicity constraints
- Reuse what already tracks this shape internally: `Scheduler.projects[id].syncing`/`dirty` already exists; the missing piece is a numeric done/total counter, not a new state machine.

## Design
1. Add a small progress counter to the per-project state the scheduler already tracks (`projectState`, `internal/daemon/scheduler.go`) — `done, total int`, updated as `RunSync`'s workers complete actions (reuse the same `writeLine`/counter-increment path already wired for `synced`/`failed`/`skipped`, just also updating a value the scheduler can read back).
2. New `Scheduler.SyncProgress(projectID string) (done, total int, syncing bool)` — a cheap, lock-protected read, no side effects.
3. Expose it two ways:
   - `ragctl status`'s existing `Status` struct (`internal/daemon/api/api.go`) gains it per-project when a sync is in flight — a human just runs `ragctl status` again.
   - A new MCP resource (not a tool — this is read-only, no side effects, the right shape for cheap polling) reporting the same for a given project.

## Inputs / Outputs
- Input: a project ID.
- Output: done/total/syncing — `syncing: false` with the last-known done/total (or zeros if never synced) when nothing is currently running.

## Failure behavior
- A project ID that's never been synced reports `syncing: false, done: 0, total: 0` — not an error.

## Tests
- Progress counter increments correctly under the real worker pool (reuse COORD-003's own concurrent-worker test fixtures) — `done` reflects actual completions, not queue position.
- `SyncProgress` for a project with nothing running reports `syncing: false` promptly (no stale "still syncing" after a run actually finishes).
- The MCP resource and `ragctl status` report the same numbers for the same in-flight sync (no drift between the two surfaces).

## Acceptance criteria
- [ ] `ragctl status` shows live done/total for any project with an in-flight sync.
- [ ] A new MCP resource reports the same, queryable independent of whatever call originally triggered the sync.
- [ ] Progress numbers are accurate under real concurrent workers, not just the old sequential loop.
