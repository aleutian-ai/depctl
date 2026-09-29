# MCP-009: MCP handshake blocked by startup project scan

**Epic:** OpenCode/MCP integration hardening
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** small

## Goal
Found live: a real user opened `opencode` against a brand-new, never-before-scanned real Go project (`gin`, with an unusually heavy dependency set — `quic-go`, `mongo-driver`, `sonic`, several JSON/validation libraries) on a separate machine and got `ragctl Operation timed out after 30000ms` on the MCP connection itself — not a specific tool call, the whole handshake. Reported with a full transcript and the daemon's own `ragctl.log`.

Root cause, found by reading the actual code path (not guessed): `runServe` (`internal/cli/serve.go`) called `startupScan` **synchronously**, before `server.Run()` even started the MCP stdio loop:

```go
startupScan(ctx, cmd.ErrOrStderr(), deps.Scan, deps.Query)  // blocked here
server := mcp.New(deps)
return server.Run(ctx, &sdkmcp.StdioTransport{})            // handshake starts only after
```

`startupScan` (MCP-006) calls `ScanProject`, which for Go runs `go list -m -json all` — a real command that hits the network (Go's module proxy) for any dependency not already in the local module cache. For a project ragctl has never seen before, with no warm cache, this has no bound. The whole MCP connection — not just a tool call, the transport handshake itself — was silently blocked behind it, with no way for the connecting client to tell the server was even alive. MCP-006's own doc comment claimed the cost "stays bounded... no dependency is cloned or embedded here" — true for embed/clone cost, false for wall-clock time, since resolution itself can hit the network.

An initial fix proposal (bound `startupScan` with a timeout) was rejected in favor of the correct architectural fix: the MCP loop must never wait on project discovery at all, regardless of how long discovery takes. A timeout still delays every fresh connection by up to its bound and leaves an awkward partial-registration state to reason about; backgrounding it removes the dependency entirely.

## Non-goals
- No new `project_state` field/schema (`initializing`/`resolved`/`syncing`/`ready`) surfaced through the MCP API — a fresh/cold project simply shows as not-yet-registered (an empty fleet) to any tool call that lands before the background scan finishes, exactly like the existing behavior for a project a user hasn't called `scan_project` on yet. This is a real, honest scope reduction from a fuller state-machine proposal, made explicitly to keep this fix narrow rather than growing it into its own epic — worth reconsidering only if "empty fleet during the first few seconds of a new project" itself proves confusing in practice.
- No change to `ScanProject`'s own resolution behavior, timeouts, or network handling — this is purely about *when* it's awaited, not how it runs.

## Simplicity constraints
- Minimal refactor: extract the scan-launch + MCP-loop-start ordering into one small function (`serveMCP`) callable identically by the real command and by a test, rather than restructuring `runServe` more broadly.

## Design
1. `runServe` launches `startupScan` via `go startupScan(...)` instead of a blocking call, then immediately builds and runs the MCP server — the MCP loop is live before the scan even needs to have started making progress.
2. `serveMCP(ctx, deps, stderr, transport)` is the extracted, testable unit: takes an `sdkmcp.Transport` parameter (real `StdioTransport` in production, an in-memory transport in tests) so a test can drive a real MCP client against it without a real daemon or real stdio.
3. Regression test (`TestServeMCPDoesNotBlockOnSlowStartupScan`, `internal/cli/serve_test.go`): a `blockingScanTrigger` fake whose `ScanProject` blocks on an unclosed channel (genuinely still in flight, not just slow) proves a real MCP client, over a real in-memory transport, can connect and successfully call `knowledge_status` while the scan remains blocked. Verified to actually catch the regression: reverting the fix (synchronous call) makes this exact test hang/time out.
4. Live, real "measured postmortem" (not part of the committed test suite, matching this session's manual/live-verification norm for real-infrastructure claims): an isolated `ragctl serve` subprocess against a real, previously-unresolved Go project (`gin`) with a genuinely empty, fresh `GOPATH`/module cache (forcing real network resolution of ~55 dependencies) — MCP connect + first `knowledge_status` call completed in **17-160ms** across two runs, while the real background scan (logged via `ragctl`'s own stderr summary line) ran and finished independently, with the connection never once waiting on it.

## Inputs / Outputs
- Input: a fresh MCP connection to `ragctl serve` for a never-before-scanned, real, dependency-heavy project.
- Output: the MCP handshake and any tool call not depending on that project's own resolved state succeed immediately, regardless of how long the background scan takes.

## Failure behavior
- Any regression back to blocking the handshake on `startupScan` is release-blocking — this is the first-use path (`install ragctl → open a new repo → MCP connection`), more fundamental than most of epic 49's stress findings.

## Tests
- `TestServeMCPDoesNotBlockOnSlowStartupScan` (new) — deterministic, fast (<1s), CI-safe; verified to fail against the pre-fix synchronous call.
- Existing `TestStartupScan*` tests (`internal/cli/serve_test.go`) — unchanged, still cover `startupScan`'s own non-fatal-error behavior; `startupScan`'s function signature and internal logic are untouched, only its caller changed.
- Live/manual: real cold-GOPATH timing run against a real, previously-unresolved Go project (see Design §4) — not part of the CI suite, matching epic 49's own established non-goal against synthetic-timing CI tests.

## Acceptance criteria
- [x] A fresh, never-before-scanned, dependency-heavy real project's MCP connection succeeds immediately, not blocked on project discovery.
- [x] `startupScan` still runs automatically (MCP-006's own guarantee preserved) — just asynchronously.
- [x] A regression test proves the ordering and is confirmed to fail against the pre-fix code.
- [x] A live, real measurement against a genuinely cold module cache confirms the fix under real network conditions, not just a synthetic fake.
- [x] Existing/warm-repo behavior is unchanged (the existing `TestStartupScan*` suite still passes untouched).

## Post-implementation note (2026-09-29)

Fixed exactly as scoped above. `internal/cli/serve.go`: `runServe` now calls the new `serveMCP` helper, which launches `startupScan` via `go startupScan(...)` and immediately proceeds to `mcp.New(deps)`/`server.Run(...)`. `internal/cli/serve_test.go` gained `blockingScanTrigger` and `TestServeMCPDoesNotBlockOnSlowStartupScan`; confirmed live that the test hangs (times out) if the fix is reverted to a synchronous call, and passes cleanly with it in place. `go build ./...`, `go vet ./...`, and the full `internal/cli` suite all pass.

Live-verified twice against a real, isolated `ragctl serve` process pointed at a genuinely fresh `GOPATH` (no cached modules at all) resolving a real `github.com/gin-gonic/gin` fixture (55 real dependencies after `go mod tidy`): MCP connect + first `knowledge_status` call completed in 17.2ms and 158.2ms across the two runs (network variance), both orders of magnitude faster than the several-second-plus real background scan running concurrently and unblocked in the same process — confirmed via the scan's own logged summary line appearing in `ragctl serve`'s stderr well after the tool call had already succeeded. This directly reproduces the reported incident's shape (a real, previously-unresolved dependency-heavy project) and confirms the handshake no longer waits on it, regardless of how long resolution itself takes on a given network.

No `project_state` field was added (see Non-goals) — a fresh project simply reports as not-yet-registered until its background scan completes, identical to today's existing "call `scan_project` yourself" fallback path MCP-006 already established for a scan failure.