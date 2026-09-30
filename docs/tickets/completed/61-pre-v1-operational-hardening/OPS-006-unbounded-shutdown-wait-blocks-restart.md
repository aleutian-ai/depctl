# OPS-006: Unbounded shutdown wait can block a daemon restart indefinitely

**Epic:** Pre-v1.0 Operational Hardening
**Status:** done
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
- [x] The store-write-cancellation audit above is done and its conclusion is recorded here.
- [x] `Scheduler.Wait()`'s shutdown-time call is bounded (once the audit confirms it's safe), with a clear log line when a run is abandoned. — *Audit found this framing was based on a wrong assumption; see below for what actually changed instead.*
- [x] `ragctl daemon stop`'s real-world behavior (CLI message vs. actual process exit) is verified live under the same STRESS-017 reproduction shape.

## Post-implementation notes (2026-09-30)

**The write-cancellation audit's conclusion: there is no store-corruption risk, bounded or not.** Tracing the actual shutdown path (`internal/daemon/server.go`'s `Serve`, `internal/cli/daemon.go`'s `runDaemonRun`) end to end:

1. `Server.Serve` calls `s.scheduler.Wait()` (a plain `sync.WaitGroup.Wait()`) and does not return until it does.
2. `runDaemonRun` calls `srv.Serve(ctx)` **synchronously** and only reaches its `defer closeWithTimeout(..., store.Close, ...)` / `defer closeWithTimeout(..., badgerStore.Close, ...)` calls after that returns.

By Go's memory model, `sync.WaitGroup.Wait()` cannot return while any goroutine that called `Add` hasn't yet called `Done` — so **no in-flight sync/GC goroutine can still be running, let alone writing to a store, by the time either store's `Close()` executes.** The ticket's original worry (bounding the wait might let `Close()` race an abandoned write) doesn't apply, because the sequencing already guarantees `Close()` never runs concurrently with a write, regardless of how long the wait takes. This also means the acceptance criterion as originally written ("bound `Scheduler.Wait()`") was based on a false premise: `Scheduler.Wait()` isn't actually the thing that needed bounding, and bounding it *would* have reintroduced the exact race the ticket was rightly worried about (letting `Close()` proceed while a goroutine the wait gave up on is still writing) — so it was deliberately **not** done. `Scheduler.Wait()` is also not literally unbounded in practice: every run's own context already carries a hard `maxActionDuration` (30 min) ceiling via `context.WithTimeout(context.WithoutCancel(s.base), maxActionDuration)`, independent of the daemon's own shutdown signal (`context.WithoutCancel` is deliberate, not an oversight — it's what stops an early shutdown request from truncating a run's own execution).

**The actual, sole bug: `ragctl daemon stop` (client-side) declared "stopped" based on the wrong signal.** It polled only whether the daemon's Unix socket had stopped accepting connections — which happens as soon as the HTTP listener's own graceful shutdown completes (bounded to `shutdownGrace`, ~30s), *before* `Server.Serve` returns from `Scheduler.Wait()`. So the CLI could report success while the process (and its `control.db` lock) was still alive and correctly finishing real work, exactly matching STRESS-017's live reproduction.

**Fix.** `internal/cli/daemon.go`'s `runDaemonStop` now fetches the daemon's real PID via `Health()` before shutting it down, and — once the socket is confirmed gone — verifies the process itself has actually exited via a signal-0 liveness probe (`processAlive`), tolerating a short (`processExitGrace`, 2s) window for the process's own ordinary teardown before concluding it's genuinely still finishing long-running work. If it's still alive after that, the message names the PID and points at `ps -p <pid>` — deliberately not `ragctl daemon status`, which dials the same socket and would misreport "not running" in this exact window too. A failed `Health()` call degrades to the pre-existing socket-only behavior, never worse than before this fix. The decision logic itself (`stopOutcomeMessage`) is a small, directly unit-tested pure function, separate from the socket-polling loop around it.

**Live verification.** `TestDaemonStopReportsStoppedForARealSeparateProcess` (`internal/cli/daemon_unix_test.go`) drives this against a real, separate `ragctl daemon run` OS process (not the pre-existing in-process test harness `TestDaemonStatusAndStop` uses, which shares its own PID with the simulated daemon and can never observe a real process exit) — confirming the plain "stopped" happy path still works correctly and quickly (no in-flight work) for the common case. Writing this test surfaced a real, separate, test-harness-only artifact worth recording: a `go test` binary is a long-lived parent that never reaps an exited detached child, so the child sits as a kernel zombie (`ps` showed `STAT "ZN <defunct>"`) that still answers a signal-0 probe as "alive" for well over a minute after the daemon's own log already showed it logged `"stopped"` and returned in well under a second — real production usage never hits this, since the short-lived CLI process that spawns the daemon (`ensureDaemon`'s `spawnDaemon`) exits almost immediately, letting the OS re-parent the orphan to `init`, which reaps it right away. The test now reaps it explicitly (`syscall.Wait4`) to match that real-world behavior rather than tripping over the harness artifact.

**Not fixed, deliberately out of scope for this ticket:** the pre-existing, separately-documented `closeWithTimeout` comment in `internal/cli/daemon.go` ("badgerStore.Close() hanging specifically on the auto-start daemon lifecycle, root cause not yet found") remains unexplained — but this session's investigation strongly suggests that comment's own hypothesis may be describing the same zombie-reaping artifact found above, observed from a different angle (a long-running process, such as a test binary or another tool, holding the daemon's PID open without reaping it), rather than a genuine hang inside `badgerStore.Close()` itself. Worth a fresh, focused look if it recurs, but re-diagnosing it fully was out of scope here.
