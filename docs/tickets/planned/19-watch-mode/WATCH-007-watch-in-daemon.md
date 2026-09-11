# WATCH-007: Watch mode moves into the daemon

**Epic:** Watch Mode
**Status:** planned
**Depends on:** WATCH-006
**Estimated size:** small

## Goal
The daemon watches every registered project's dependency manifests using the existing `internal/watch` package. A debounced change marks the project dirty through the scheduler (WATCH-006), and the daemon re-resolves and syncs it automatically. `ragctl watch` is removed.

## Non-goals
- Any change to `internal/watch`'s detection or debounce logic. It's reused as-is, and `SetProjects`/`Run`/`Events` already provide everything needed.
- Watching source files or registry manifests.

## Simplicity constraints
- One goroutine reads `watcher.Events()` and calls `scheduler.Request(ev.ProjectID, SyncOptions{Resolve: true})`, ignoring the returned channel. Nothing else.
- Reuse `resyncProject` (`internal/cli/watch.go`) for runs with `Resolve` set. It re-resolves, stores the resolution, then calls `RunSync`, exactly what `ragctl watch` does today, now against the daemon's already-open stores. Runs without `Resolve` (plain `ragctl sync`, WATCH-008) call `RunSync` only.

## Design
- `Engine` gains `Projects(ctx) ([]watch.Project, error)`, built the way `loadWatchProjects` does it today, and `Sync(ctx, projectID, opts, out io.Writer) (api.SyncResult, error)`, which calls `resyncProject` when `opts.Resolve` is set and `RunSync` otherwise. It is the function the scheduler runs.
- On startup, when `watch.enabled` is true, the daemon:
  - builds `watch.New(cfg.Watch.Debounce, logf)`, calls `SetProjects(engine.Projects())`, and starts `Run`;
  - prints "watching N project(s), debounce X".
- When `watch.enabled` is false, the daemon starts without a watcher and logs that watch is disabled. The rest of the daemon still serves.
- Project-set refresh:
  - `SetProjects` is called once a minute, and immediately after any API call that registers or re-resolves projects (scan/resolve, WATCH-008).
  - No retry-on-lock path. The daemon owns the lock, so `changeLoop`'s `ErrLocked` retry, `watchLockRetry`, and per-change store opening all go away.
- Delete `ragctl watch`, meaning `newWatchCmd`, `runWatch`, `changeLoop`, `lockedWriter`, and `syncChangedProject`. Keep `resyncProject`, moved to wherever the `Engine` implementation lives in `internal/cli`.
  - If `watch` is invoked, it should print "`ragctl watch` was replaced by `ragctl daemon run` (watching is on while `watch.enabled` is true)". A stub is the smallest way to do that. Delete it in a later release.

## Inputs / Outputs
- Input: `internal/watch` `ChangeEvent`s.
- Output: scheduler requests; per-change log lines (`<time> <root>: go.mod changed`, `resolved <root>: N dependencies`, sync output).

## Failure behavior
- Resolve or sync failure for one project: the scheduler logs it (WATCH-006), and watching continues for all projects.
- An unwatchable root: `internal/watch` already logs and skips it.

## Tests
- End to end (unix build tag, real fsnotify, real daemon on a temp socket):
  - scan a Go fixture through the API, then edit its `go.mod` to add a dependency;
  - the stored resolution gains it and a sync ran.
  - Adapted from `TestWatchResyncsProjectWhenGoModChanges`.
- Repeated file events collapse into one sync: several rapid writes to `go.mod` produce one resync. Count calls through a test hook on the `Engine`.
- A file change during a running sync causes exactly one follow-up. Block the first resync until a second edit has been debounced.
- A project whose resolve fails (a broken `go.mod`) is logged, and another project's change still syncs.
- `watch.enabled: false`: the daemon runs, no watcher starts, and the API still answers.
- Remove `internal/cli/watch_test.go` / `watch_unix_test.go` cases that tested the deleted loop. Keep `TestResyncProjectPicksUpNewDependency`.

## Acceptance criteria
- [ ] The daemon watches registered projects via the unchanged `internal/watch`, debounced by `watch.debounce`.
- [ ] Manifest changes go through the scheduler; resolve and sync happen automatically.
- [ ] `ragctl watch` no longer opens stores; it's removed or reduced to the pointer message.
- [ ] `docs/internal/watch.md`, `docs/internal/cli.md`, and `docs/architecture.md`'s `ragctl watch` flow now describe the daemon.
