# WATCH-004: ADR — single-owner daemon

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-003
**Estimated size:** small

## Goal
Write `docs/adr/ADR-011-single-owner-daemon.md`, recording the decision that ragctl is moving from multi-process direct store access to a single-owner daemon with local RPC clients, because watch, MCP serving, and CLI commands otherwise contend on long-lived database locks. WATCH-005..011 implement it.

## Non-goals
- Any code. This ticket is the decision record only.
- launchd/systemd installation, TCP, auth, multi-host. The ADR lists them as explicitly out of scope.

## Simplicity constraints
- Same shape as ADR-006/ADR-010: Context, Decision, Consequences, Related.
- Record only decisions the implementing tickets actually depend on; don't pre-design later work.

## Design
**Context** (facts from the code today, not hypotheticals):
- bbolt takes an exclusive file lock for as long as `control.db` is open.
- `ragctl serve` is a stdio MCP server. Every agent session starts its own `serve`, which holds the lock for the whole session (`internal/cli/serve.go`). Two agent sessions block each other.
- `ragctl watch` opens the stores per change and retries every 30s while `serve` holds the lock. So it can't sync while an agent session is open, which is exactly when the user is coding.
- `status`/`doctor`/`sync`/`gc` fail with `ErrLocked` after 2s while `serve` runs (epic 18's lock-timeout fix).

**Decision:**
1. **Invariant:** while the daemon runs, it is the only process that opens or mutates `control.db`, Badger, or the vector backend.
2. **The process:** `ragctl daemon run`, foreground, long-running.
3. **Local-only transport:** HTTP/JSON over a Unix domain socket at `<dir of control.db>/ragctld.sock`, created with mode 0600. No TCP listener and no auth; file permissions are the access control.
4. **Ownership is proven by the bbolt lock, not the socket.** At startup the daemon opens `control.db` first. If that succeeds, no other daemon can be alive, so any existing socket file is stale and gets removed.
5. **Store-touching commands require the daemon, and start one if it's absent.** A client with no daemon spawns `ragctl daemon run` detached and waits briefly for the socket (WATCH-012); `daemon.autostart: false` turns that off, and the command then fails with an actionable error. Either way there's exactly one code path — never a fallback to opening the stores directly — so the invariant holds by construction. Without auto-start the operational problem this epic set out to fix would just move: instead of keeping `ragctl watch` open, the user would have to keep `ragctl daemon run` open, and now everything would depend on it.
   - Exceptions: `init` (creates the stores and refuses if a daemon is running), `config validate` and `registry` (never touch the stores), and `doctor` (reports "daemon not running" as a check result).
   - Concurrent auto-starts need no coordination: both spawned daemons race for the bbolt lock, the loser sees it's already owned and exits, and both clients connect to the winner.
6. **Syncs are serialized globally for v1.** Generations are per dependency and shared across projects, so two projects that reference the same dependency version could race to build the same generation. bbolt also allows only one writer at a time. Revisit only if serial sync proves too slow in practice.
7. **Collapsing sync requests:** per project, a request that arrives mid-sync sets `dirty`, and at most one follow-up sync runs.
8. **`ragctl serve` becomes a stdio MCP proxy.** It keeps `internal/mcp`'s tool definitions, and those tools call the daemon client instead of `query.Service` directly.
9. **Watch stops being its own process.** `internal/watch` feeds the daemon's scheduler; `ragctl watch` survives only as a deprecated shim that ensures the daemon is running and exits.

**Consequences:**
- Positive: no lock contention; watch syncs while agents are connected; one sync scheduler.
- Negative / tradeoffs:
  - A background process now exists that the user never explicitly started, and it stays running until `ragctl daemon stop` or a reboot. `ragctl daemon status` and `ragctl daemon run` (foreground) are the ways to see and debug it.
  - The daemon inherits the environment (PATH, HOME) of whichever command auto-started it, which is what its resolvers will use.
  - A daemon crash is recovered by the next command auto-starting a new one, but work in flight is lost.
  - Command output for sync/gc is streamed over the socket instead of written directly.
- Amends ADR-010: the "long-running `serve` daemon" becomes `ragctl daemon run`; `serve` is a proxy.

## Inputs / Outputs
- Output: `docs/adr/ADR-011-single-owner-daemon.md`.
- Also add a one-line "Amended by ADR-011" note to ADR-010's Related section.

## Failure behavior
N/A.

## Tests
N/A (doc only).

## Acceptance criteria
- [ ] ADR-011 exists, Status: Accepted, covering every Decision item above.
- [ ] ADR-010 references ADR-011.
- [ ] WATCH-005..012 cite ADR-011 rather than restating its reasoning.
- [ ] The ADR frames this as a process-model migration: the goal is the smallest implementation that establishes single store ownership **without redesigning domain behavior** (existing operation → daemon handler → existing implementation; existing CLI → daemon client). Storage, sync, query, resolver, MCP, watch, and GC logic stay where they are.

## Post-implementation note
ADR-011 exists (`docs/adr/ADR-011-single-owner-daemon.md`, Accepted). Status was left at "planned" here after the ADR itself was written and accepted — a documentation gap in this ticket file, not in the actual work.
