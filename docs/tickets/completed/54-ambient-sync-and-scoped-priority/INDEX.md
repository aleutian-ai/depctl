# Epic: Ambient Sync and Scoped Priority

Directly downstream of epic 53 (foreground JIT isolation) and the live STRESS-005/COORD-003 benchmark: real numbers now confirm a full bulk sync of a real, large project takes tens of minutes to an hour-plus even with concurrency — it will never become a "wait a few seconds" operation. That confirms the product decision this epic implements: **`ragctl sync` (full, untargeted) should be the deliberate default** — triggered automatically the first time a project is registered, not something an operator has to remember to run separately — while whatever the agent is *actively* working on always preempts to the front, and stays fast, regardless of how much background work is queued behind it.

This requires three things that don't fully exist yet, found by enumerating every way an agent currently expresses "I care about this dependency" against the real MCP tool surface (`internal/mcp/tools.go`):

1. **A pull-side progress query.** `search_dependency_docs`'s JIT path already pushes progress via MCP progress tokens (WATCH-013) and returns a bounded `still_running`/`ReportedSoFar`/`Total` response (calibrated against a real prior discovery — opencode's own tool-call timeout) — but there's no way to *ask* "how far along is this" independent of the call that started it. Checked `ragctl status`'s real fields (`internal/daemon/api/api.go`): only coarse aggregate counts exist, no per-project sync progress.
2. **Full sync isn't triggered on first scan.** Checked `internal/daemon/watch.go`: watch mode only re-syncs when a manifest file *changes later* (a real fsnotify event). Initial project registration resolves dependencies but never triggers a sync at all today.
3. **Multi-dependency prioritization doesn't exist, and `explain_call_site` has zero JIT wiring.** `search_dependency_docs` is the only tool with a JIT-sync fallback, and it's scoped to one dependency name at a time. `explain_call_site` — the tool that resolves a call site in the agent's own code, arguably the single most natural "working on this file" signal — has no sync trigger at all: if the dependency isn't synced yet, it just returns nothing useful. And the underlying request-coalescing has a real, previously-unexamined imprecision: `mergeOptions` in `internal/daemon/scheduler.go` merges two *different* single-dependency filters into an *empty* one (meaning "sync everything") rather than a set — safe (never silently drops a dependency), but imprecise (asking about several specific imports could accidentally balloon into a full untargeted sync if two of those requests happen to coalesce).

## Tickets
- [SCOPE-001](SCOPE-001-sync-progress-query.md) — the pull-side progress surface.
- [SCOPE-002](SCOPE-002-auto-trigger-full-sync-on-registration.md) — full sync becomes the deliberate default on first `ragctl scan`.
- [SCOPE-003](SCOPE-003-multi-dependency-scoped-sync.md) — `SyncOptions.Dependency string` → a real set, fixing the merge-to-everything imprecision; the foundation SCOPE-004 needs.
- [SCOPE-004](SCOPE-004-prioritize-file-tool.md) — a new `prioritize_file` MCP tool (parse a Go file's real imports, map to resolved dependencies, prioritize the set) and wiring `explain_call_site` into the same JIT mechanism `search_dependency_docs` already has.

## Non-goals
- No multi-ecosystem import parsing in the first pass — Go only, matching ragctl's own established one-ecosystem-at-a-time convention (see epics 20/21 in backlog for Python/Node, unbuilt). `prioritize_file` degrades to "no matched imports, nothing to prioritize" for a non-Go file, not an error.
- No package/directory-level prioritization (enumeration item #4 — several related files at once) in this epic's first pass — file-level (SCOPE-004) is the concrete, scoped unit; a directory-level version is a natural, separate follow-up once file-level is proven, not assumed necessary yet.
- No change to `sync_project`'s existing single-dependency behavior or its bounded-wait/still-running contract — SCOPE-003/004 add a new *set*-shaped path alongside it, they don't replace what's already correct.

## Status
All four tickets are implemented, unit/integration tested, and now verified live against a real daemon and a real agent client.

- SCOPE-001/SCOPE-002: verified via the mem0 end-to-end run (`docs/tickets/completed/56-npm-pypi-fallback-manifest`) — `ragctl scan` against a real 22-sub-project repo fired ambient sync automatically, with real `ragctl status` calls showing accurate live per-project progress throughout a 15-minute run.
- SCOPE-003/SCOPE-004: verified via `TestPrioritizeFileAndExplainCallSiteOverARealDaemon` (`internal/cli/scope_004_live_test.go`) — a real `ragctl serve` subprocess, a real MCP client over stdio, and two genuinely different real Go modules. `explain_call_site` triggered a fresh JIT sync and returned real evidence for a never-synced dependency; `prioritize_file` then correctly matched and synced both of a file's real imports as one set, with the newly-synced one confirmed independently searchable afterward.

**Epic moves to `completed/`.**
