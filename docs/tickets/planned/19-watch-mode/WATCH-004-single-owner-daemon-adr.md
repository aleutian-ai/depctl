# WATCH-004: ADR — single-owner daemon

**Epic:** Watch Mode
**Status:** planned
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
5. **Store-touching commands require the daemon.** When it isn't running they fail with an actionable error. Each command then has one code path, not a "daemon or direct" pair, and the invariant holds by construction.
   - Exceptions: `init` (creates the stores and refuses if a daemon is running), `config validate` and `registry` (never touch the stores), and `doctor` (reports "daemon not running" as a check result).
6. **Syncs are serialized globally for v1.** Generations are per dependency and shared across projects, so two projects that reference the same dependency version could race to build the same generation. bbolt also allows only one writer at a time. Revisit only if serial sync proves too slow in practice.
7. **Collapsing sync requests:** per project, a request that arrives mid-sync sets `dirty`, and at most one follow-up sync runs.
8. **`ragctl serve` becomes a stdio MCP proxy.** It keeps `internal/mcp`'s tool definitions, and those tools call the daemon client instead of `query.Service` directly.
9. **Watch is removed as a separate command.** `internal/watch` feeds the daemon's scheduler.

**Consequences:**
- Positive: no lock contention; watch syncs while agents are connected; one sync scheduler.
- Negative / tradeoffs:
  - You have to start the daemon yourself until a later ticket adds an OS service or auto-start.
  - A daemon crash makes store-touching commands unavailable until it's restarted.
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
- [ ] WATCH-005..011 cite ADR-011 rather than restating its reasoning.
