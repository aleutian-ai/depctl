# WATCH-013: MCP progress notifications for `sync_project`/`scan_project`

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-010 (serve MCP proxy), the MCP-bootstrapping fix (`docs/scratch/mcp-bootstrapping.md`)
**Estimated size:** small

## Goal
An agent calling `sync_project` on a project with many dependencies currently gets nothing back until the whole sync finishes — no visibility into whether it's making progress or hung. Wire the daemon's existing per-dependency streamed output through to the MCP protocol's built-in progress-notification mechanism, so a client that requested progress (via a progress token) sees one update per dependency as it completes.

## Non-goals
- No new wire protocol between the daemon and its CLI clients — the daemon already streams one NDJSON line per completed action (`RunSync`'s `OK`/`FAIL`/`SKIP` lines, `scanAndResolve`'s `new`/`existing`/`resolved` lines); this only relays what already exists.
- No guaranteed-exact `Total` for `scan_project` — a scan discovers projects as it walks, so there's no reliable upfront count. `sync_project`'s `Total` is a best-effort lookup (`GetProjectDependencies`), not a wire-level guarantee either — if that lookup fails, progress still reports (just without a fraction).
- No behavior change for a client that doesn't ask for progress (no progress token on the tool call) — this is purely additive; `NotifyProgress` is only ever called when `CallToolParams.GetProgressToken()` is non-nil.

## Simplicity constraints
- No changes to `internal/daemon` or the wire format at all — this is entirely a client-side (`internal/cli/query_client.go`) and MCP-adapter-side (`internal/mcp`) change.
- One small buffering adapter (`lineWriter`) turns the existing `io.Writer` streaming convention into a per-line callback; `client.stream`'s existing `fmt.Fprintln(out, line.Log)` already writes one already-newline-terminated server line per call, so the adapter's buffering is a defensive fallback, not load-bearing.

## Design
`internal/mcp/server.go`: `SyncTrigger`/`ScanTrigger` gain a `progress func(line string)` parameter (may be nil):
```go
type SyncTrigger interface {
    SyncProject(ctx context.Context, projectID string, progress func(line string)) (synced, failed, skipped int, err error)
}
type ScanTrigger interface {
    ScanProject(ctx context.Context, root string, progress func(line string)) (projectIDs []string, summary string, err error)
}
```

`internal/cli/query_client.go`: `daemonSyncTrigger`/`daemonScanTrigger` pass a `lineWriter(progress)` (a small `io.Writer` that splits on `\n` and calls `progress` once per complete line) instead of `nil`/a plain buffer as the streamed-output target for `client.Sync`/`client.Resolve`.

`internal/mcp/tools.go`: a `progressReporter(ctx, req, total)` helper returns `nil` when the caller didn't attach a progress token (`req.Params.GetProgressToken() == nil`) — meaning zero cost for a client that never asks — and otherwise returns a closure that increments a running count and calls `req.Session.NotifyProgress` with `{ProgressToken, Progress: done, Total: total, Message: line}` per line. `syncProjectHandler` looks up `Total` via a `GetProjectDependencies` call before starting (best-effort — 0 if it fails); `scanProjectHandler` passes `Total: 0` (indeterminate), since a scan's dependency count isn't known upfront.

## Inputs / Outputs
- Input: an MCP `tools/call` for `sync_project` or `scan_project`, optionally carrying a progress token in `_meta.progressToken`.
- Output: unchanged final tool result, plus zero or more `notifications/progress` messages during the call when a token was supplied.

## Failure behavior
- No progress token on the call: identical behavior to before this ticket — no notifications sent, no extra work done beyond a no-op nil check.
- `GetProjectDependencies` lookup fails (e.g., project not yet resolved): `Total` is 0 (client renders as indeterminate); the sync itself still proceeds and reports its real error through the normal path if it fails.
- `NotifyProgress` itself erroring (e.g., a slow/disconnected client): ignored per the SDK's own documented pattern — a progress notification is best-effort, never allowed to fail the actual tool call.

## Tests
- `lineWriter` splits a multi-line single `Write` call into one callback invocation per line, and correctly buffers a `Write` call that ends mid-line.
- `progressReporter` returns `nil` when the request has no progress token (verifies zero notifications sent).
- `syncProjectHandler`/`scanProjectHandler`, given a request with a progress token and a fake trigger that calls its `progress` callback a few times, produce the same count of `NotifyProgress` calls with strictly increasing `Progress` values.
- Existing `TestSyncProjectHandlerDisabledByDefault`/`TestSyncProjectHandlerEnabledCallsTrigger` and the new `scan_project` tests keep passing with the new `progress` parameter (`nil` for calls with no session/token available in a bare unit test).

## Acceptance criteria
- [x] A progress-token-carrying `sync_project` call streams one notification per synced/skipped/failed dependency.
- [x] A progress-token-carrying `scan_project` call streams one notification per discovered/registered project.
- [x] A call with no progress token behaves exactly as before (no new work, no notifications).
- [x] Verified end-to-end against a real MCP client that registers a progress handler, not just at the handler-unit-test level.

## Post-implementation note

Verified live against a real `depctl serve` subprocess (a genuine MCP client over `CommandTransport`, not in-memory transports) on a Go project with 20 dependencies: `scan_project` streamed 4 notifications (`Total: 0`, since scan doesn't know a project/dependency count upfront), then `sync_project` streamed 23 (`Total: 20`, precomputed via `GetProjectDependencies`) as each dependency failed against a local Ollama with no embedding model pulled — proving the mechanism end to end, including the `Total` lookup and per-line relay, independent of whether the underlying sync itself succeeds.

One cosmetic, harmless detail worth recording: `RunSync`'s blank separator line and final `"N synced, N failed, N skipped"` summary line are relayed as progress ticks too (since `lineWriter` has no way to distinguish "a real per-dependency line" from "a trailing summary line" — it only sees line boundaries), so `Progress` overshoots `Total` by a small, fixed amount (2-3) at the very end of a `sync_project` call. Not fixed, since the final tool result (with the real `Synced`/`Failed`/`Skipped` counts) always arrives immediately after and is authoritative — a client rendering `Progress`/`Total` live sees a brief "105%" at the very end rather than a wrong final answer.
