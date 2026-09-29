# MCP-007: Distinguish a real sync failure from a bounded-wait timeout

**Epic:** OpenCode/MCP integration hardening
**Status:** done — root-caused, fixed, and live-verified 2026-09-27
**Depends on:** none
**Estimated size:** small

## Problem
`syncProjectHandler`'s bounded-wait design (WATCH-018, `internal/mcp/tools.go`) only produces the graceful `{still_running: true, ...}` response on its own timer (`mcpSyncWaitBound`, 90s) firing. If the underlying `sync.SyncProject(...)` call returns a **real error** before that timer fires, it was wrapped and returned as a raw tool-level error instead. A raw tool-level error and a client's own timeout can look identical to an agent with no further context, even though one means "the sync is fine, just not done yet" and the other means "something is genuinely wrong." Live-found: a reported `(32001)` timeout-shaped error on one Node plugin's `sync_project` call, in the same session where a different project's `sync_project` call correctly returned a graceful `still_running` response.

## Root cause, confirmed by reading the real code (not the live Node session — see below)
Tracing `RunSync` end to end (`internal/cli/sync.go`) confirms per-dependency failures (bad tag, missing manifest, etc.) are already correctly absorbed into the `failed` count and never reach the handler's error path — that part already works as designed, matching this ticket's own non-goal. The only things that can produce a real `err` here are setup-level failures before any per-dependency work starts: `computePlans`'s project/registry lookup, or the scheduler shutting down.

But two compounding bugs meant *any* such error — however rare — could never be classified or made actionable, unlike every other tool's errors:
1. **`internal/daemon/stream.go`'s error branch dropped error identity entirely.** The non-streaming path (`writeError`, used by search/status/etc.) already calls `errorKind(err)` to attach a `Kind` string (e.g. `"project_not_found"`) so the client can reconstruct a matching sentinel via `errors.Is` (this exact mechanism was built for WATCH-019/020's own live-found wire-identity bug). But `stream()` — used by `sync_project`, `scan_project`, and `gc`, since these are long-running and need progress lines — only ever sent `api.StreamLine{Error: err.Error()}`, a bare string; the struct had no `Kind` field at all. The client's `stream()` method (`internal/daemon/client/client.go`) then reconstructed it as a plain `errors.New(line.Error)` — sentinel identity provably, unconditionally lost for every sync/scan/GC error, confirmed by reading the code, not by needing to reproduce a live failure.
2. **`syncProjectHandler` also bypassed `toolError()` entirely** (`fmt.Errorf("sync_project: %w", r.err)`), so even a sentinel that did survive the wire wouldn't get the same friendly, actionable message every other tool already gives.
3. Separately, `computePlans`'s project-lookup (`internal/cli/plan.go`) returned bbolt's raw, generic `ErrNotFound` rather than `query.ErrProjectNotFound` — so even after fixing 1 & 2, the one concrete, plausible real failure mode (a stale/deleted `project_id` reaching `sync_project` mid-session) still wouldn't classify as anything nameable.

This satisfies the ticket's own "reproduce the specific failure mode" gate differently than originally planned: rather than reproducing the exact live Node-plugin symptom (not practical in this environment — no live OpenCode session or real npm registry outage to hand), the fix is proven by a deterministic, real HTTP-boundary test (`TestDaemonSyncTriggerProjectNotFoundIsClassified`, `internal/cli/query_client_test.go`) that fails without the fix and passes with it — the same kind of proof `TestDaemonQueryServiceRoundTripsThroughRealDaemon`'s sibling assertions already use for the non-streaming path.

## Fix
1. `internal/daemon/api/api.go` — `StreamLine` gets a `Kind string` field, mirroring `Error`'s existing one.
2. `internal/daemon/stream.go` — the error branch now sends `api.StreamLine{Error: err.Error(), Kind: errorKind(err)}`.
3. `internal/daemon/client/client.go` — `stream()` reconstructs `&RemoteError{Message: line.Error, Kind: line.Kind}` instead of `errors.New(line.Error)`, matching `responseError`'s existing non-streaming reconstruction.
4. `internal/cli/query_client.go` — `daemonSyncTrigger.SyncProject` now runs its error through the existing `wrapQueryError` helper (already used by `daemonQueryService`, just not previously by this trigger).
5. `internal/mcp/tools.go` — `syncProjectHandler` routes `r.err` through `toolError(err)` instead of a bare `fmt.Errorf`.
6. `internal/cli/plan.go` — `computePlans`'s project lookup wraps a not-found `store.GetProject` error as `query.ErrProjectNotFound` instead of leaving it as bbolt's generic `ErrNotFound`.
7. `internal/mcp/prioritize.go` — `prioritizeFileHandler` had the identical bare-`fmt.Errorf`-bypass bug (same `ensureDependencies` → raw-error pattern), fixed the same way with `toolError(syncErr)`. `explain_call_site`'s equivalent path (`justInTimeSyncedEvidence`) was left alone — it deliberately discards `syncErr` in favor of the original, well-understood `NotSyncedError`, per its own existing comment.

## Non-goals
- No change to the `still_running` path itself — it already works correctly (confirmed live, the Go project's own sync in the same reported session).
- Not assumed to be the same root cause as MCP-004 (the `sync_progress` staleness bug) — related in symptom (both look like "sync seems to have failed silently"), not confirmed to share a cause; unaffected by this fix.
- No change to how per-dependency failures are counted or reported — confirmed already correct and out of scope.

## Acceptance criteria
- [x] `api.StreamLine` carries error `Kind` across the wire for `sync_project`/`scan_project`/`gc`, matching the non-streaming path's existing behavior.
- [x] `daemonSyncTrigger.SyncProject`'s error is classified via `wrapQueryError`, same as `daemonQueryService`'s methods.
- [x] `syncProjectHandler` and `prioritizeFileHandler` route real errors through `toolError`, same as every other MCP tool handler.
- [x] `computePlans`'s project-not-found error is classified as `query.ErrProjectNotFound`.
- [x] Live-verified against a real daemon over the actual HTTP boundary: `TestDaemonSyncTriggerProjectNotFoundIsClassified` (`internal/cli/query_client_test.go`) — a real `ragctl daemon run` subprocess, a real `sync_project`-shaped RPC against an unregistered `project_id`, asserting `errors.Is(err, query.ErrProjectNotFound)`.
- [x] Unit-verified at the handler level: `TestSyncProjectHandlerClassifiesSentinelErrors` (`internal/mcp/tools_test.go`).
- [x] Full native and Linux (`hack/test-linux.sh`) suites pass.
