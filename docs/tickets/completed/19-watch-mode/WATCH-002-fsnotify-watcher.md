# WATCH-002: fsnotify watcher

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-001
**Estimated size:** medium

## Goal
Build a debounced fsnotify-based watcher over the manifest paths from WATCH-001 that emits a "project changed" event stream, handling renames/recreation/removal correctly.

## Non-goals
- What happens after an event fires (resolve/plan/sync) — that's WATCH-003.

## Simplicity constraints
- One watcher goroutine, one debounce timer per project (map keyed by project ID) — not a generic event-bus/pub-sub system.
- Debounce via a simple "reset timer on new event" pattern (`time.AfterFunc`), not a custom scheduler.

## Design
Package: `internal/watch`. Dependency: `github.com/fsnotify/fsnotify`.

```go
type Watcher struct {
    fsw      *fsnotify.Watcher
    debounce time.Duration // config: watch.debounce, default 2s
    events   chan ChangeEvent
}
type ChangeEvent struct {
    ProjectID string
    Paths     []string
}

func NewWatcher(cfg Config) (*Watcher, error)
func (w *Watcher) Watch(ctx context.Context, projects []Project) error // adds fsnotify.Add for each WatchPaths() entry
func (w *Watcher) Events() <-chan ChangeEvent
```

Handle fsnotify quirks explicitly:
- **Rename**: many editors/package managers replace lockfiles via rename-into-place. On `fsnotify.Rename` for a watched path, re-`Add` the watch on that path (it may point to a new inode) after a short delay, and still treat it as a change event.
- **Recreation**: on `Remove` followed by `Create` of the same path within the debounce window, coalesce into a single change event (not a removal + separate creation).
- **Project removal**: if a project is deregistered (no longer in the registry), call `fsw.Remove` for its paths; do not emit events for it further.

Filesystem callbacks must only enqueue into `events`/debounce timers — never perform network or resolver work directly on the fsnotify callback goroutine.

## Inputs / Outputs
- Input: list of `Project` (with resolved `WatchPaths`).
- Output: debounced `ChangeEvent` stream on `Events()`.

## Failure behavior
- `fsnotify.Add` failure for one path (e.g. permission denied) logs a warning and continues watching the rest; does not abort the whole watcher.
- Watcher goroutine panics/errors are recovered and logged, watcher keeps running for other projects.

## Tests
- Single file write triggers exactly one debounced event after the debounce window, even with multiple rapid writes.
- Rename-into-place (simulate `os.Rename`) still triggers a change event and watch remains active on the new file.
- Project removal stops further events for its paths.
- No network/blocking work happens on the fsnotify callback goroutine (verified via a fake blocking downstream consumer not blocking the watcher).

## Acceptance criteria
- [x] Debounce configurable via `watch.debounce` (default 2s).
- [x] Rename and recreation handled without losing watch coverage.
- [x] Filesystem callback never performs blocking network work.

## Post-implementation note

Lives in `internal/watch/watcher.go`. The API is `New(debounce, logf)`, `SetProjects(projects)`, `Run(ctx)`, and `Events()` rather than the sketched `NewWatcher(cfg)` plus `Watch(ctx, projects)`. `SetProjects` replaces the whole watched set, which is how project removal works: a root missing from the new set is unwatched and its pending change is dropped. `Run` is separate so the caller owns the goroutine and its shutdown.

The biggest change is **watching directories instead of files**, following fsnotify's own guidance. A watch held on a file silently stops working when a tool replaces that file by renaming a temp file over it, which is how many package managers and editors write lockfiles. So each project's root directory is watched (fsnotify watches aren't recursive) and events are filtered to manifest names. That handles all three quirks this ticket lists without special-case code: rename-into-place is just a create event for the manifest name; remove-then-create lands in the same debounce window as one change; and there's nothing to re-`Add`. It also catches a lockfile being created for the first time, which per-file watches can't.

Debounce: each project has one pending "burst" (changed paths plus a `time.AfterFunc` timer), reset on every new event. `Run` only records paths and resets timers under a mutex. The send to `Events()` happens on the timer's goroutine, so a slow consumer delays delivery but never blocks the filesystem-event loop. A timer superseded by project removal or a later burst checks that it's still current and does nothing. A panic while handling one event is recovered and logged; an unwatchable root is logged and skipped.

Tests: `internal/watch/watcher_test.go` — five rapid writes produce one event; rename-into-place triggers and the watch keeps working after; remove-then-recreate is one event; non-manifest files never trigger; a removed project stops emitting; a blocked consumer doesn't stop another project's change being seen; an unwatchable root doesn't affect others. Run under `-race` ten times in a row on macOS (kqueue) and five times on Linux (inotify) via `hack/test-linux.sh`.
