# WATCH-003: `ragctl watch`

**Epic:** Watch Mode
**Status:** done
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
- [x] Supports foreground daemon mode only (no OS service install in this ticket).
- [x] Filesystem event → resolve → plan → sync, executed off the filesystem-event goroutine. (Reconciled: no job worker exists, so sync runs on watch's own change loop through the same `RunSync` as `ragctl sync`; see note.)

## Post-implementation note

Lives in `internal/cli/watch.go`.

**No job worker exists, so nothing is enqueued.** This ticket assumed PLAN-003 had set up a job system with a worker that executes queued sync jobs. It didn't: the `jobs` bucket holds only GC bookkeeping, `ragctl sync` runs inline through `RunSync`, and nothing polls for queued work. Building a queue and worker just for `watch` would add exactly the "separate scheduler" this ticket's simplicity constraint rules out. Instead, each change is handled on `watch`'s own change loop: re-resolve the project, store the new resolution, then call `RunSync` for that project, the same code `ragctl sync --project <id>` runs. That keeps the ticket's two real requirements. Nothing blocking runs on the filesystem-event goroutine (WATCH-002 only enqueues), and there's one sync code path, not a parallel "watch sync". Changes are processed one at a time, so two syncs never run concurrently.

Other decisions:
- **Stores are opened per change, not for the process lifetime.** An idle `watch` holds no lock, so `status`, `sync`, and the rest keep working while it runs. `ragctl serve` does hold the lock for its lifetime (see epic 18's lock-timeout fix), so a change that arrives while `serve` runs is retried every 30 seconds until the lock is free, rather than dropped. If the lock is held at startup, `watch` exits with the lock error.
- **The project list is re-read** after every change and once a minute, so projects registered by `ragctl scan` or removed while `watch` runs are picked up without a restart.
- **Config:** `watch.enabled: false` makes the command refuse to start, as `server.mcp.enabled` does for `serve`; `watch.debounce` sets the debounce window. Both keys already existed but nothing read them.
- **Shutdown:** SIGINT/SIGTERM stops new changes being taken. A change already in progress gets a context that isn't cancelled with the signal, so it finishes instead of leaving a half-built generation. A second Ctrl-C restores default signal handling and exits immediately.
- A failed change (resolver error, sync error) is logged and the loop continues.

Tests: `internal/cli/watch_test.go` — the loop continues after a failed change, retries a change that hit the store lock, and lets an in-flight change finish after cancellation with an uncancelled context. `resyncProject` against a real Go fixture picks up a newly added dependency and records its reference. `watch.enabled: false` is refused, and a locked store fails with `ErrLocked`. `internal/cli/watch_unix_test.go` — the real command exits cleanly on an actual SIGTERM; and end to end, with the command running, editing a scanned project's `go.mod` produces a change line, a re-resolve to 2 dependencies, and a sync, all through real fsnotify.
