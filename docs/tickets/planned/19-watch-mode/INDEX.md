# Epic: Watch Mode

Automatic local refresh: watch only dependency manifest files (never source code) and drive resolve → plan → sync when they change. Corresponds to Milestone 18 in the implementation plan.

## Tickets
- [WATCH-001](WATCH-001-manifest-watch-list.md) — Compute the minimal per-project set of manifest files to watch.
- [WATCH-002](WATCH-002-fsnotify-watcher.md) — Debounced fsnotify watcher handling rename/recreate/removal, emitting change events.
- [WATCH-003](WATCH-003-ragctl-watch.md) — `ragctl watch` foreground daemon: change event → resolve → plan → enqueue sync.
