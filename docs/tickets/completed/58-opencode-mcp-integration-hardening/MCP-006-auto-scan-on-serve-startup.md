# MCP-006: Auto-scan on `serve` startup

**Epic:** OpenCode/MCP integration hardening
**Status:** done — implemented and live-verified 2026-09-27
**Depends on:** none
**Estimated size:** small

## Decision, 2026-09-27 (revised)
**Auto-register + auto-report status on `serve` startup; never auto-sync.** `runServe` (`internal/cli/serve.go`) already builds `deps.Scan` and `deps.Query` before calling `server.Run` — add one step there: call `scan_project`'s own underlying `ScanTrigger.ScanProject(ctx, ".", nil)` against the server's cwd, then a `knowledge_status`-equivalent summary, and print both to stderr (the stdio transport reserves stdout for the MCP protocol itself — same reason `"ragctl MCP server starting..."` already goes to `cmd.ErrOrStderr()`).

This does **not** trigger any sync. Registration + status is cheap: manifest parsing and version resolution (`go list`, pip/npm registry lookups), not cloning or embedding doc sources — the expensive work only happens once `sync_project` or a JIT-sync-on-miss (`search_dependency_docs`, WATCH-019/020) actually runs, unchanged. What the dependency actually gets built from — machine-local cache, a configured mirror/checkout, or a fresh clone — is entirely epic 43's existing resolver logic, untouched by this ticket either way.

This supersedes the 2026-09-27 same-day "no behavior change" decision recorded first in this ticket. That decision treated "auto-scan" as if it implied the expensive half (sync/embedding) and rejected it on cost grounds; walking a concrete proposed flow against the real tool set (`internal/mcp/tools.go`) showed the cheap half (registration + status) and the expensive half (sync) are already cleanly separated in the existing tools, and only the cheap half needs to move to automatic — the agent (or the human, via the agent) still decides what to sync and how much, using `sync_project`'s existing dependency-name filter (`internal/mcp/tools.go:168`).

## Problem, confirmed accurate
`ragctl serve` does not automatically scan its own working directory. `scan_project` (`internal/mcp/tools.go`) is a tool the agent must explicitly call — confirmed by reading `runServe` (`internal/cli/serve.go`): its only use of `os.Getwd()` is for `explain_call_site`'s symbol-graph resolver, not project registration. In the reported session, the agent correctly called `scan_project` on its own (helped by the tool's own description) and it worked — but the user experience still requires that explicit step, and the agent has no way to know up front how much of the project's dependency graph is already searchable without first spending a tool call to find out.

## Design
1. In `runServe`, after `ensureDaemon`/health check and before `server.Run`: call the same scan path `scan_project` uses (`deps.Scan.ScanProject(ctx, ".", progress)`), scoped to the server's own cwd — mirrors `scan_project`'s existing default-root convention exactly, so this is not new scanning behavior, just an existing call moved earlier.
2. Follow with a `knowledge_status`-equivalent read (`deps.Query`) and print a short human-readable summary to stderr: project(s) found, dependency count, how many already have synced knowledge.
3. Startup failure handling: a scan error here must not fail `serve` startup — same non-fatal posture `callSiteResolver` already uses for `os.Getwd()` failures. Log the error to stderr and continue; the agent can always retry `scan_project` manually.
4. No new MCP tool, no new daemon endpoint — this only changes when an already-existing scan call happens, from "first agent tool call" to "server startup."

## Options weighed
1. **Chosen.** Auto-scan + auto-status-report on startup; sync stays fully agent/user-driven (existing `sync_project` scoping).
2. Auto-scan cwd lazily, on first tool call that needs project context (mirrors JIT-sync-on-miss). Rejected: every project-scoped tool already requires a `project_id` up front (no `cwd` field anywhere), so there's no existing tool-call shape a lazy scan could hook into without inventing new tool surface.
3. No behavior change at all. Rejected on reconsideration: the "real cost" concern that motivated this only applies to sync/embedding, not to the cheap registration+status half, which the agent otherwise has to spend its own first tool call discovering — worth automating even without evidence of the agent failing to call `scan_project` on its own.
4. Auto-scan **and** auto-sync everything unconditionally on startup. Rejected: violates the JIT/lazy-build principle already established by WATCH-019/020 and epic 43's cache-reuse design — would pay full clone+embed cost for every dependency in every `serve` session regardless of whether anything is ever asked about it, and would ignore the user's own choice of how much to pull.

## Non-goals
- No change to `sync_project`'s own behavior or its dependency-name filter — already supports "all / some / one."
- No new tool descriptions or protocol surface — this only moves an existing scan earlier and adds a status print.
- No auto-sync of any kind, even for small projects — always left to an explicit `sync_project` call (by the agent, prompted by the human user, or otherwise).

## Acceptance criteria
- [x] `runServe` calls the scan path against its own cwd before entering `server.Run`, using the same default-root convention `scan_project` already uses (`internal/cli/serve.go`'s new `startupScan`).
- [x] A scan failure at startup is logged to stderr and does not prevent `serve` from starting (`startupScan` returns after logging; `TestStartupScanFailureIsNonFatal`).
- [x] A short status summary (projects found, dependency count, synced-vs-total) is printed to stderr on successful scan (`TestStartupScanReportsStatusSummary`); zero registered project IDs skips the `Status` call rather than reporting on an empty fleet (`TestStartupScanNoProjectsSkipsStatus`).
- [x] No change to `sync_project`, `search_dependency_docs`, or any JIT-sync-on-miss behavior — `startupScan` only calls `ScanTrigger.ScanProject` and `QueryService.Status`, neither of which touch sync.
- [x] Live-verified against a real polyglot repo: `TestServeOverRealStdioTransport` (`internal/cli/serve_smoke_test.go`) spawns a real `ragctl serve` subprocess over stdio and confirms `knowledge_status` sees the registered project correctly with the startup scan now running ahead of it — passed unchanged.

## Implementation notes (2026-09-27)
`startupScan(ctx, stderr, scan, q)` in `internal/cli/serve.go`, called from `runServe` right after `deps := mcp.Deps{...}` and before `mcp.New(deps)`/`server.Run`. Unit-tested directly in `internal/cli/serve_test.go` against minimal `mcp.ScanTrigger`/`mcp.QueryService` fakes (nil trigger, scan error, zero-projects, success, status error) — five cases, all passing. Full `internal/cli` and `internal/mcp` suites green, including the real-subprocess smoke test.
