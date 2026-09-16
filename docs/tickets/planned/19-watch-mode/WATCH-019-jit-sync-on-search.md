# WATCH-019: Sync-on-demand for a single missing dependency

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-018 (bounded wait — this ticket's JIT sync reuses `SyncTrigger`, not a new mechanism)
**Estimated size:** small

## Goal
Today, `search_dependency_docs` against a dependency with no active generation just fails with `"no synced knowledge for this version yet — run `ragctl sync`"` (`toolError`, `internal/mcp/tools.go:396`) — the agent has to separately call `sync_project`, which (with no dependency filter) syncs the *entire* project's dependency tree. For a large project, that's exactly the cost this whole session measured as prohibitive (115 dependencies, 14+ minutes, zero results) to answer a question about one library. The underlying single-dependency sync mechanism already exists end to end (`SyncOptions.Dependency`, `api.SyncRequest.Dependency`, the CLI's `--dependency` flag) — it's just never been exposed over MCP. Close that gap: when `search_dependency_docs` hits `ErrNoActiveGeneration` for a specific, named dependency, trigger a sync scoped to *just that dependency* and retry, instead of requiring a separate, coarser tool call first.

## Non-goals
- No change to the Scheduler, `RunSync`'s action ordering, or anything about a sync already in progress for the project — that's WATCH-020. This ticket only covers the case where *no* sync is currently running for the project; WATCH-020 covers the case where a background one already is.
- No bounded-wait/partial-response treatment for this JIT sync path (unlike `sync_project`, WATCH-018) — a single dependency's sync is a much smaller, typically fast operation, and adding that complexity here isn't justified until real usage shows a single-dependency sync itself exceeding a client's timeout. If that turns out to be needed, it's a straightforward follow-up reusing WATCH-018's exact pattern.
- No JIT-sync for modes/queries with no identifiable single dependency (`ModeAllRetained`'s ecosystem-unfiltered case, or a query with no `dependency` argument at all) — there's nothing specific to sync in that case; the existing error stands.
- Respects the exact same `server.mcp.enable_sync_tool` gate `sync_project` already uses — a deliberately read-only MCP session (`enable_sync_tool: false`) must not have `search_dependency_docs` silently start syncing on its behalf. When disabled, behavior is unchanged from today.

## Simplicity constraints
- `SyncTrigger.SyncProject` (`internal/mcp/server.go:43`) gains one new parameter, `dependency string` — reusing the exact mechanism `SyncOptions.Dependency` already provides end to end, not a new sync path.
- `searchDependencyDocsHandler` gains exactly one new branch: on `errors.Is(err, query.ErrNoActiveGeneration)` with a non-empty `in.Dependency` and sync enabled, trigger the JIT sync and retry the search once — not a retry loop, not multiple attempts.

## Design
`internal/mcp/server.go`:
```go
type SyncTrigger interface {
	// dependency, when non-empty, scopes the sync to just that package
	// (mirroring SyncOptions.Dependency/`ragctl sync --dependency`) —
	// empty means "the whole project," today's existing behavior.
	SyncProject(ctx context.Context, projectID, dependency string, progress func(line string)) (synced, failed, skipped int, err error)
}
```
`internal/cli/query_client.go`'s `daemonSyncTrigger.SyncProject` threads `dependency` into `api.SyncRequest{ProjectID: projectID, Dependency: dependency}` — the field already exists on the wire type, just never populated from this call site.

`internal/mcp/tools.go`:
- `SyncProjectIn` gains `Dependency string \`json:"dependency,omitempty" jsonschema:"optional — limit the sync to one package instead of the whole project"\`` — `syncProjectHandler` passes `in.Dependency` through unchanged otherwise (WATCH-018's bounded-wait logic is untouched, just now dependency-scoped when requested).
- `searchDependencyDocsHandler(query QueryService, sync SyncTrigger, enableSync bool)` (gains two params):
```go
result, err := svc.SearchKnowledge(ctx, query.Query{ProjectID: in.ProjectID, Text: in.Query, Dependency: in.Dependency, Mode: mode})
if err != nil {
	if errors.Is(err, query.ErrNoActiveGeneration) && in.Dependency != "" && enableSync && sync != nil {
		if _, failed, _, syncErr := sync.SyncProject(ctx, in.ProjectID, in.Dependency, nil); syncErr == nil && failed == 0 {
			result, err = svc.SearchKnowledge(ctx, query.Query{ProjectID: in.ProjectID, Text: in.Query, Dependency: in.Dependency, Mode: mode})
		}
		// syncErr != nil or failed > 0: fall through to the original
		// ErrNoActiveGeneration below — the JIT attempt failed, report
		// the original, well-understood error rather than a confusing
		// second one.
	}
	if err != nil {
		return nil, SearchDependencyDocsOut{}, toolError(err)
	}
}
```
`progress` is passed as `nil` here deliberately — `search_dependency_docs` has no progress-token plumbing of its own (unlike `sync_project`), and adding it is unnecessary complexity for what's meant to be a small, single-dependency sync.

`internal/mcp/server.go`'s `registerTools` call site updates to `searchDependencyDocsHandler(deps.Query, deps.Sync, deps.EnableSyncTool)`.

## Inputs / Outputs
- Input: unchanged (`SearchDependencyDocsIn`); `SyncProjectIn` gains an optional `dependency` field.
- Output: `search_dependency_docs` succeeds directly (no separate `sync_project` call needed) for a project whose target dependency simply hasn't been synced yet, when nothing else is preventing it (backend readiness, config, etc.).

## Failure behavior
- JIT sync itself fails (embedding/vector backend unreachable, no registry manifest, etc.): the original `ErrNoActiveGeneration`-derived message is what the agent sees — not the JIT sync's own error, which would be a confusing surprise for a call the agent didn't know was happening. (The JIT sync's own failure is still visible via `daemon status`/`doctor` for a human debugging it.)
- `enable_sync_tool: false`: identical to today — `search_dependency_docs` never triggers anything, same as before this ticket.
- The requested dependency doesn't actually exist for this project (a typo, or a real `ErrDependencyNotFound`): unaffected — that's a different error path, never reaches the JIT-sync branch.

## Tests
- A project with a registered-but-never-synced dependency: `search_dependency_docs` succeeds directly, and the fake `SyncTrigger` records exactly one call scoped to that dependency (not the whole project).
- `enable_sync_tool: false`: `search_dependency_docs` reports the original `ErrNoActiveGeneration`-derived error; the fake `SyncTrigger` is never called.
- JIT sync itself fails: `search_dependency_docs` reports the original error, not the sync failure.
- `search_dependency_docs` with no `dependency` argument, hitting `ErrNoActiveGeneration` some other way: unaffected — no JIT-sync attempt (nothing to scope it to).
- `daemonSyncTrigger.SyncProject` (real-daemon integration, extending an existing `TestSyncProjectHandlerEnabledCallsTrigger`-style test) passes a non-empty `dependency` through to a real `api.SyncRequest.Dependency`.

## Acceptance criteria
- [x] `search_dependency_docs` against a project with an unsynced-but-resolvable dependency succeeds without a separate `sync_project` call, when sync is enabled.
- [x] The triggered sync is scoped to exactly the requested dependency — never the whole project.
- [x] `enable_sync_tool: false` sessions are entirely unaffected — no implicit sync ever happens for a deliberately read-only session.
- [x] `sync_project`'s own `dependency` field (new) lets an agent explicitly scope a sync request the same way the CLI's `--dependency` flag already does.

## Post-implementation note
Shipped exactly per spec, with zero surprises — the underlying mechanism really was already fully wired end to end (`SyncOptions.Dependency` → `api.SyncRequest.Dependency` → `handleSync`), so this was purely a matter of threading one new parameter through `SyncTrigger`'s interface, `daemonSyncTrigger`, `SyncProjectIn`, and adding the retry-once branch in `searchDependencyDocsHandler`. `TestDaemonSyncTriggerDependencyFilterReachesRealSyncOptions` (real daemon) proves the filter genuinely reaches `RunSync` server-side, not just a mock: a non-matching dependency name produces a true no-op (0/0/0) even though the project's one real dependency would otherwise fail at the backend step. Full suite green.
