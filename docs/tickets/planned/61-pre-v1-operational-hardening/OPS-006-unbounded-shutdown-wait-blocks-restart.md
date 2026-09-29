# OPS-006: Unbounded shutdown wait can block a daemon restart indefinitely

**Epic:** Pre-v1.0 Operational Hardening
**Status:** planned
**Depends on:** none
**Estimated size:** small-to-medium (needs a safety audit, not just a patch — see below)

## Goal
Found live during epic 49's STRESS-017 (daemon restart during an active MCP session, 2026-09-29). `ragctl daemon stop` reports `"stopped"` as soon as its shutdown RPC is accepted, but the actual daemon process can keep running — still holding the bbolt `control.db` lock — for as long as any in-flight sync/GC run takes to finish, with **no timeout at all**. `internal/daemon/server.go`'s shutdown path bounds the HTTP listener's own graceful shutdown to `shutdownGrace` (30s), but the subsequent `s.scheduler.Wait()` call (`internal/daemon/scheduler.go:416`, `s.inFlight.Wait()`) has no bound whatsoever.

Live-confirmed via a real `kill -QUIT` on a genuinely stuck daemon process (STRESS-017's fixture setup accidentally triggered a real ambient sync of several dependencies via a nested-`$HOME` mistake): the resulting Go runtime stack dump showed the main goroutine parked at `scheduler.go:416` for over 3 real minutes, with real `RunSync`/`BuildCoordinator.Build` goroutines still active underneath it, while `ragctl daemon status` already reported "not running" (the socket had stopped accepting) and any attempt to start a new daemon failed with `control.db is locked but no daemon answers on socket; another ragctl process holds it`.

## Non-goals
- Not a fix for anything about the restart/reconnect behavior itself — STRESS-017 separately confirmed that part already works well (a live MCP session's next call succeeds again, with no reconnect logic needed, once any daemon rebinds the same socket).
- Not a redesign of the shutdown sequence — the goal is bounding the existing wait, not rearchitecting it.

## Why this wasn't fixed same-day (unlike OPS-004/005)
A same-day fix was drafted and reverted: bounding `Scheduler.Wait()` with `shutdownGrace` (mirroring the HTTP listener's own bound) is a small code change, but it isn't obviously *safe* on its own. `Server.Serve`'s shutdown path cancels `runCtx` and then lets any run already in flight finish against the **still-open** control/data stores, before `runDaemonRun`'s deferred `Close()` calls run afterward. If the wait is bounded and the shutdown proceeds to close the stores while an abandoned sync/GC goroutine is still actively writing to bbolt/Badger underneath it, that's a real store-corruption or panic risk — worse than the UX gap being fixed. Whether that's actually safe depends on whether every real code path in `generation.Build`/`RunSync`/`BuildCoordinator` promptly stops its own store writes once `runCtx` is cancelled, which hasn't been audited. That audit is this ticket's real prerequisite, not a rushed patch.

## Design (for whoever picks this up)
1. Audit `generation.Build`, `RunSync`, and `BuildCoordinator.Build`/`doOnce` for what happens to an in-progress bbolt/Badger write when the context they're running under is cancelled mid-operation — does it stop before the next store write, or can a write land after cancellation with nothing watching for it?
2. If that's already safe (or made safe), bound `Scheduler.Wait()` the same way `srv.Shutdown` is bounded (a `WaitTimeout(shutdownGrace)`-style helper was already prototyped and is easy to re-add), and log clearly when a run is abandoned rather than finishing naturally.
3. Separately (independent of the above, and safe regardless): consider whether `ragctl daemon stop`'s CLI should distinguish "shutdown requested" (the RPC was accepted) from "confirmed stopped" (the process has actually exited), so a user isn't told `"stopped"` while the process — and its lock — may still be alive.

## Inputs / Outputs
- Input: this ticket's own live finding (STRESS-017's post-implementation note) as the reproduction case.
- Output: either the store-write-cancellation audit clears the bounded-wait fix for a future ticket, or it surfaces that store writes need their own cancellation hardening first.

## Failure behavior
- Shipping a bounded wait without confirming write-cancellation safety, and later hitting real store corruption from an abandoned in-flight write, would be strictly worse than today's (confusing but non-corrupting) unbounded wait.

## Tests
- Whatever the eventual fix is, needs a live/integration test that puts a real (or realistically slow, e.g. artificially delayed) sync in flight, calls shutdown, and confirms both: (a) the process exits within a bounded time, and (b) the store is left in a fully consistent state afterward (no partial generation, no doctor complaints) — not just a fast exit.

## Acceptance criteria
- [ ] The store-write-cancellation audit above is done and its conclusion is recorded here.
- [ ] `Scheduler.Wait()`'s shutdown-time call is bounded (once the audit confirms it's safe), with a clear log line when a run is abandoned.
- [ ] `ragctl daemon stop`'s real-world behavior (CLI message vs. actual process exit) is verified live under the same STRESS-017 reproduction shape.
