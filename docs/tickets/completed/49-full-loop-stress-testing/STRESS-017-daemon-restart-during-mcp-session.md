# STRESS-017: Daemon restart during an active MCP session

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** small

## Goal
Restart the daemon (both gracefully and via `kill -9`) while a real `ragctl serve` MCP session is connected and mid-conversation with it, and confirm the client either reconnects/autostarts cleanly on the next tool call, or fails with a clear, actionable error — never a silent hang or a confusing partial response.

## Non-goals
- No change to `ensureDaemon`'s autostart logic unless this finds a real gap — verification first.

## Simplicity constraints
- Reuses VERIFY-001's real-subprocess-plus-real-client pattern.

## Design
1. Spawn a real `ragctl serve` subprocess, connect a real MCP client, issue one successful tool call to confirm the session is live.
2. `ragctl daemon stop` (graceful) — issue another tool call through the same, still-connected MCP session. Record what happens: does `ragctl serve` autostart a fresh daemon for the next call (matching its documented "invisible after `ragctl init`" behavior), or does the call fail?
3. Repeat with `kill -9 <daemon pid>` (ungraceful) instead of `daemon stop`.
4. In both cases, confirm a subsequent NEW `ragctl serve` session (not the one that was live during the restart) works normally afterward.

## Inputs / Outputs
- Input: a live MCP session, a daemon restart (graceful and ungraceful) mid-session.
- Output: pass/fail on the existing session's behavior (clean reconnect/autostart or clear error, never a hang) and on a fresh session working normally afterward.

## Failure behavior
- A hang, a confusing partial/malformed response, or a fresh session also failing afterward is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] A tool call issued through an already-connected MCP session, after the daemon was restarted underneath it (both gracefully and via `kill -9`), either succeeds via clean reconnect/autostart or fails with a clear, actionable error — never a hang.
- [x] A fresh MCP session started after either restart works normally.

## Post-implementation note (2026-09-29)

Confirmed via code research first (per this session's now-standard practice of verifying architecture before designing a live test): `ragctl serve` calls `ensureDaemon` exactly once at startup, then every tool call redials the daemon's Unix socket fresh via a stateless HTTP client (`internal/daemon/client`) — there's no persistent connection and no explicit reconnect/retry logic in the tool-call path. This meant the live test could reveal something the code alone couldn't answer: since each call is a fresh dial, does an *already-open* MCP session's next call succeed again on its own once some other process restarts the daemon on the same socket, even though `serve` itself never re-runs `ensureDaemon`?

Built an isolated fixture (real Qdrant, isolated `$HOME`, `watch.enabled: false` to avoid any ambient project discovery — see the real finding below for why this mattered) and a throwaway driver (`hack/stress017/main.go`, real `sdkmcp` client over `CommandTransport`, deleted after use) that: (A) confirmed a live session's first call succeeds, (B) ran `ragctl daemon stop` gracefully then called through the *same* session, (C) externally restarted the daemon (a separate `ragctl daemon run`, not via `serve`'s own `ensureDaemon`) and called through the *same* session again, (D) `kill -9`'d that daemon and called again, (E) restarted externally again and called again, (F) opened a brand-new `serve` session afterward.

**Result — better than the code alone suggested:**
- (B) and (D): both a graceful stop and a `kill -9` produced an immediate, clear, actionable tool-level error (`ragctl daemon is not running (socket ...)`) — never a hang, in either case.
- (C) and (E): once the daemon was restarted by *any* means (not necessarily `serve`'s own autostart), the **same already-open MCP session's next call just worked**, with no reconnect logic needed — because each call is a fresh socket dial, not a persistent connection, "reconnecting" is free. This is a genuinely good, non-obvious property worth documenting: an MCP client doesn't need to restart its `serve` session after a daemon restart as long as something (a supervisor, a human, autostart from a different terminal) brings the daemon back on the same socket.
- (F): a brand-new session after both restarts worked normally, as expected.

**Real finding (via a genuine, if initially accidental, live reproduction):** while first setting up the isolated fixture, nesting the isolated `$HOME` under the same directory tree the daemon was watching (`watch.enabled` was still on at that point) caused `go run`'s own module-cache downloads to be ambient-scanned as real "projects," triggering real background syncs. Calling `ragctl daemon stop` while one of those was genuinely in flight produced a `daemon stop` CLI that printed `"stopped"` immediately, while the actual daemon **process kept running for over 3 real minutes afterward**, still holding the bbolt `control.db` lock — confirmed directly via `kill -QUIT` on the live process, which dumped a real Go runtime stack showing the main goroutine parked at `internal/daemon/scheduler.go:416` (`Scheduler.Wait()`, i.e. `s.inFlight.Wait()`) with real `RunSync`/`BuildCoordinator.Build` goroutines still active underneath it. Root cause: `Server.Serve`'s shutdown path (`internal/daemon/server.go:196-208`) bounds the HTTP listener's own shutdown to a 30-second `shutdownGrace`, but the subsequent `s.scheduler.Wait()` call has **no timeout at all** — it blocks for however long any in-flight sync/GC run takes to finish naturally, however many minutes that is for a real, large dependency. Meanwhile the CLI's `daemon stop` reports success (`"stopped"`) as soon as the shutdown *RPC* is accepted, not once the process has actually exited and released its lock — so a user who trusts that message and immediately tries to restart the daemon gets the confusing `control.db is locked but no daemon answers on socket; another ragctl process holds it` error for as long as that in-flight work takes.

This is a real, previously-undocumented gap distinct from what the ticket set out to test (which passed cleanly). Considered a same-day fix (bounding `Scheduler.Wait()` with `shutdownGrace`, matching the HTTP listener's own bound) and prototyped it, but backed it out: `Server.Serve`'s shutdown cancels `runCtx` and lets an in-flight sync/GC run finish against the *still-open* control/data stores before `runDaemonRun`'s deferred `Close()` calls run — forcibly abandoning that goroutine on a timeout without first confirming every real code path (`generation.Build`, `RunSync`, `BuildCoordinator`) actually stops its own bbolt/Badger writes promptly on context cancellation risks a genuinely worse bug (a write racing an in-flight `Close()`) traded for a UX one. That safety question needs its own real audit, not a rushed patch under this ticket — filed as `OPS-006` instead, un-fixed, per this epic's own non-goals ("fixed in the same ticket if small, filed as its own follow-up if not").
