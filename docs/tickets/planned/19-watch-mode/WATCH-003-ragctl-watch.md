# WATCH-003: `ragctl watch`

**Epic:** Watch Mode
**Status:** planned
**Depends on:** WATCH-002, PLAN-003
**Estimated size:** small

## Goal
Implement `ragctl watch` as a foreground daemon: consume WATCH-002's change events and drive resolve → plan → queue sync for the affected project.

## Non-goals
- OS service installation (systemd/launchd unit files) — explicitly deferred to a later, separate ticket per the plan.
- Background/detached daemon mode — v0.1 is foreground-only.

## Simplicity constraints
- `ragctl watch` is a thin loop: `for event := range watcher.Events() { resolve; plan; enqueue sync job }`. No separate scheduler process.
- Reuse PLAN-003's existing sync execution path (as an enqueued job) rather than building a parallel "watch sync" code path.

## Design
`cmd/ragctl` `watch` command.

```bash
ragctl watch
```

Flow per `ChangeEvent`:
```text
1. re-run the resolver for the affected project (RES-001/GO-002 etc.)
2. compute plan (PLAN-001) against current active knowledge
3. enqueue a SyncJob (bbolt jobs bucket) — do not run sync inline on the watch goroutine
```

A separate worker (already established by PLAN-003 / job system) picks up and executes queued sync jobs. `ragctl watch` itself just resolves, plans, and enqueues; it blocks on `ctx.Done()` (SIGINT/SIGTERM) for graceful shutdown.

## Inputs / Outputs
- Input: `ChangeEvent` stream from WATCH-002.
- Output: enqueued `SyncJob` records; stdout log lines per detected change.

## Failure behavior
- Resolver failure for one project logs and continues watching others; does not crash the watch loop.
- SIGINT/SIGTERM triggers graceful shutdown: stop accepting new fsnotify events, let in-flight resolve/plan finish, then exit 0.

## Tests
- Simulated change event results in exactly one enqueued sync job with the correct project/dependency delta.
- Resolver error for one project doesn't stop watch loop from processing subsequent events for other projects.
- SIGTERM during idle watch exits cleanly within a bounded time.

## Acceptance criteria
- [ ] Supports foreground daemon mode only (no OS service install in this ticket).
- [ ] Filesystem event → resolve → plan → enqueue sync, with actual sync execution handled by the existing job worker (async, not inline).
