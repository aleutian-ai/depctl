# WATCH-018: Bounded wait for `sync_project`, with a partial-progress response

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-013 (MCP progress notifications — this ticket reuses that mechanism's existing per-line counting, doesn't replace it)
**Estimated size:** small

## Goal
Live-found gap, running a real project (`xwing-keyfile`, one sizable dependency — `cloudflare/circl`) through opencode end to end: `sync_project`'s MCP tool call exceeded opencode's own client-side tool-call timeout on a first sync, and the agent had no way to know the sync was still succeeding server-side (it was — confirmed via `depctl describe` immediately afterward: 1418 chunks, `status=complete`). The agent gave up, answered from general knowledge, and only recovered because a human manually told it to check again. `sync_project` should never make a calling MCP client wait indefinitely for a result that can legitimately take up to `maxActionDuration` (30 minutes, `internal/daemon/scheduler.go:25`) — it should return within a small, predictable window every time, with a response shape that tells the agent whether the sync is done or still working, so it knows to check back rather than assume failure.

## Non-goals
- No change to `depctl sync` (the CLI command) — it should keep blocking until completion; that's the correct, expected behavior for a terminal command a human is watching.
- No change to `internal/daemon/handlers.go`'s `handleSync`, the `Scheduler`, `RunSync`, or `SyncTrigger`'s interface signature (`internal/mcp/server.go:43`) — the daemon already does the right thing (a sync survives its triggering request disconnecting, via `context.WithoutCancel(s.base)` at `scheduler.go:328`; the client's own `longRunningRequestTimeout`, 35 minutes, already comfortably exceeds `maxActionDuration`). The entire fix lives in `internal/mcp/tools.go` — a client-side (from the daemon's perspective) wait-bound, not a server-side behavior change.
- No new daemon HTTP endpoint, no polling API. The existing read-only tools (`list_project_dependencies`, `knowledge_status`) are already the correct way to check a sync's current state — this ticket doesn't duplicate that, it just tells the agent to use them.
- No attempt to report a precise `synced`/`failed`/`skipped` breakdown in a partial (still-running) response — deriving that would mean parsing `RunSync`'s human-readable log lines (`internal/cli/sync.go`'s `"OK    %s %s\n"`/`"FAIL  %s %s: %v\n"` etc.) as a data contract, which is exactly the kind of brittle coupling (breaks silently if the log wording ever changes) this codebase avoids. A partial response reports only a coarse "N of M dependency actions reported so far" — the authoritative per-dependency state is always the existing read tools, not a parsed approximation.
- `scan_project` isn't touched — it completed fast and cleanly in both live tests this session; applying the same pattern there is a reasonable future ticket if it's ever actually observed to be slow, not a preemptive one here.

## Simplicity constraints
- The bound-and-race logic is entirely inside `syncProjectHandler` (`internal/mcp/tools.go:281`): run the existing blocking `sync.SyncProject(...)` call in a goroutine, `select` between it finishing and a timer.
- Reuse the exact "one progress-callback invocation = one unit of progress" counting `progressReporter` (`tools.go:60`) already established for WATCH-013's `done`/`total` — don't invent a second counting mechanism. A small wrapper around the existing `progress func(line string)` callback increments a counter on every invocation, independent of whether a progress token was supplied (today, `progressReporter` returns `nil` and nothing is counted when the caller didn't ask for progress notifications — this ticket's counter must work even then, since it needs a running tally regardless of whether the *client* wants MCP progress notifications).

## Design
`internal/mcp/tools.go`'s `SyncProjectOut` gains three fields:
```go
type SyncProjectOut struct {
	Synced        int    `json:"synced,omitempty"`
	Failed        int    `json:"failed,omitempty"`
	Skipped       int    `json:"skipped,omitempty"`
	StillRunning  bool   `json:"still_running,omitempty"`
	ReportedSoFar int    `json:"reported_so_far,omitempty"`
	Total         int    `json:"total,omitempty"`
	Note          string `json:"note"`
}
```
`Synced`/`Failed`/`Skipped` are only meaningful when `StillRunning` is false (the normal, fast-path case — most syncs). When `StillRunning` is true, `ReportedSoFar`/`Total` describe coarse progress instead, and `Note` is a distinct string (not `securityNote`) explaining what happened and what to do about it.

```go
// mcpSyncWaitBound bounds how long syncProjectHandler waits for
// SyncTrigger.SyncProject before returning a partial, still-running
// response instead — so an MCP tool call never blocks a calling
// client past this, no matter how long the underlying sync legitimately
// takes (up to maxActionDuration, internal/daemon/scheduler.go).
//
// Live-found calibration: opencode's own tool-call timeout (unrelated
// to anything depctl controls — the MCP spec doesn't standardize one)
// was observed at roughly 5 minutes in the session that found this gap.
// 90s sits well under that with real margin, while still being long
// enough that a typical sync of a handful of small-to-medium
// dependencies finishes within it and never hits the still-running path
// at all — only genuinely large first-time syncs (a big repo like the
// cloudflare/circl case) do. A var, not a const, so tests can shorten
// it; not a value to over-tune, since other MCP clients' own timeouts
// are unknown and may differ from opencode's.
var mcpSyncWaitBound = 90 * time.Second

const syncStillRunningNote = "the sync is still running in the background and was not cancelled by this call returning — wait a bit and call list_project_dependencies or knowledge_status to check current state, or call sync_project again (concurrent requests for the same project collapse into one, so this is never wasted work)"

func syncProjectHandler(query QueryService, sync SyncTrigger, enabled bool) sdkmcp.ToolHandlerFor[SyncProjectIn, SyncProjectOut] {
	return func(ctx context.Context, req *sdkmcp.CallToolRequest, in SyncProjectIn) (*sdkmcp.CallToolResult, SyncProjectOut, error) {
		if !enabled || sync == nil {
			return nil, SyncProjectOut{}, errors.New("sync_project is disabled by config (server.mcp.enable_sync_tool: false)")
		}
		total := 0
		if query != nil {
			if deps, err := query.GetProjectDependencies(ctx, in.ProjectID); err == nil {
				total = len(deps)
			}
		}

		var reported atomic.Int64
		notify := progressReporter(ctx, req, total)
		progress := func(line string) {
			reported.Add(1)
			if notify != nil {
				notify(line)
			}
		}

		type result struct{ synced, failed, skipped int; err error }
		done := make(chan result, 1)
		// Detached deliberately: if the select below times out and this
		// handler returns, the sync must keep running exactly as it
		// already does when a client disconnects mid-sync — see
		// scheduler.go:328's identical context.WithoutCancel(s.base) for
		// the same reasoning, one layer down.
		bgCtx := context.WithoutCancel(ctx)
		go func() {
			synced, failed, skipped, err := sync.SyncProject(bgCtx, in.ProjectID, progress)
			done <- result{synced, failed, skipped, err}
		}()

		select {
		case r := <-done:
			if r.err != nil {
				return nil, SyncProjectOut{}, fmt.Errorf("sync_project: %w", r.err)
			}
			return nil, SyncProjectOut{Synced: r.synced, Failed: r.failed, Skipped: r.skipped, Note: securityNote}, nil
		case <-time.After(mcpSyncWaitBound):
			return nil, SyncProjectOut{
				StillRunning:  true,
				ReportedSoFar: int(reported.Load()),
				Total:         total,
				Note:          syncStillRunningNote,
			}, nil
		}
	}
}
```
The tool's own description (`internal/mcp/tools.go:43`, currently ending "...attach a progress token to the call to receive one notification per dependency as it completes") gains a sentence explaining the `still_running` shape directly, so a model reads the contract before it ever hits it: *"A first sync of a large dependency can take longer than this call's own bound — if the response has `still_running: true`, the sync did not fail and is continuing on the server; check `list_project_dependencies`/`knowledge_status` or call `sync_project` again rather than treating it as an error."*

## Inputs / Outputs
- Input: unchanged (`SyncProjectIn{ProjectID}`).
- Output: the existing complete-result shape for any sync finishing within `mcpSyncWaitBound` (the common case — most dependencies, and every dependency already covered by an existing active generation), or the new partial shape for one that doesn't.

## Failure behavior
- The underlying sync genuinely fails (not a timeout — a real error from `SyncTrigger.SyncProject`) after the bound already fired and a partial response was already sent: the error is only ever observed by the background goroutine, which has nothing left to report it to (the tool call already returned). This is an accepted, deliberate trade-off — a real failure past that point surfaces the next time the agent checks `list_project_dependencies` (a dependency will show `has_active_generation: false`) or runs `sync_project` again (which will report the actual failure, since by then the scheduler's dedup means a repeat request either observes the same run finish or starts a fresh one).
- A sync that completes in, say, 95s against a 90s bound: the tool call returns the partial `still_running` response even though the real one was only 5s away — an accepted imprecision of any fixed bound, not something to chase with a smarter adaptive timeout.

## Tests
- `syncProjectHandler` returns the normal complete response when the underlying `SyncTrigger.SyncProject` finishes before `mcpSyncWaitBound` (existing `TestSyncProjectHandlerEnabledCallsTrigger`-style fake trigger, no behavior change expected here — a regression guard).
- `syncProjectHandler` returns `StillRunning: true` with the right `ReportedSoFar`/`Total` when a fake `SyncTrigger` blocks past a shortened `mcpSyncWaitBound` (test var override, mirroring `qdrantStartupTimeout`'s own test-shortening pattern) — and that the fake trigger's own goroutine is still allowed to run to completion afterward (not cancelled), asserted via a channel/flag the fake sets when it actually finishes.
- `reported`'s counter increments once per progress-callback invocation regardless of whether a progress token was supplied on the request (covers both `TestSyncProjectReportsProgressWhenTokenPresent`'s and `TestSyncProjectSendsNoProgressWithoutToken`'s existing fixture shapes, extended to also assert the counter).
- The background goroutine's context is confirmed detached from the request context: a fake `SyncTrigger` that checks `ctx.Err()` after the handler has already returned (simulating the request context being cancelled once the tool call completes) must see no cancellation.

## Acceptance criteria
- [x] `sync_project` never blocks a calling MCP client past `mcpSyncWaitBound`, regardless of how long the underlying sync takes.
- [x] A sync that finishes within the bound reports exactly the same complete response shape as today — zero behavior change for the common case.
- [x] A sync that doesn't finish in time reports `still_running: true` with coarse progress and a `Note` explaining what to do next, and the underlying sync is not cancelled by the tool call returning.
- [x] The tool's own description documents the `still_running` response shape, so a model can act on it correctly without a human explaining it mid-session (the exact gap this ticket was found from).

## Post-implementation note
Shipped exactly per spec. `mcpSyncWaitBound` landed at 90s (calibrated against the ~5-minute opencode timeout observed live, corrected from an initial too-aggressive 20s guess before implementation). `SyncProjectOut` gained `StillRunning`/`ReportedSoFar`/`Total` as `omitempty` fields, so the common (fast) case's JSON shape is unchanged from before this ticket. The background goroutine uses `context.WithoutCancel(ctx)`, mirroring `scheduler.go`'s own pattern exactly as designed. New test: `TestSyncProjectHandlerReturnsPartialWhenBoundExceeded`, using a new `blockingSyncTrigger` fake (channel-released, records `ctx.Err()` at completion) to deterministically prove both the partial-response shape and that the background sync survives the handler returning, without any wall-clock-dependent flakiness. Full suite green.
