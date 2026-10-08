# ADR-011: Single-owner daemon with local RPC clients

**Status:** Accepted
**Date:** 2026-09-11

## Context

depctl's persistent state lives in bbolt (`control.db`) and Badger. bbolt takes an **exclusive file lock for as long as the database is open**, so at most one process can have the control store open at a time. Every depctl process opened those stores directly, which turned out not to compose:

- `depctl serve` is a stdio MCP server. Agent clients launch one process per session, and it holds the lock for the whole session (`internal/cli/serve.go`). Two agent sessions can't run at once.
- `depctl watch` (epic 19's WATCH-003) worked around this by opening the stores only while handling a change, and retrying every 30 seconds when the lock was held. In practice that means it can't sync while an agent is connected — exactly when the user is editing dependencies.
- `status`, `doctor`, `sync`, `plan`, `deps`, `describe`, and `gc` fail with `ErrLocked` after a 2-second timeout while `serve` runs (epic 18's OPS-001 lock-timeout fix, which made the failure fast and legible but didn't remove it).

Every one of these is a symptom of the same thing: **several processes want long-lived access to a store that only one process can hold.** Two narrower fixes were considered and rejected:

- **Make `serve` open the stores per tool call** (what `watch` does). Badger's open cost — vlog replay — would land on every query, and `serve` and `watch` would still contend.
- **Keep the retry loop and tune the timings.** Contention is structural; tuning only changes how long each loser waits.

## Decision

**While the daemon is running, it is the only process that opens or mutates depctl's persistent stores.** Everything else becomes a client of it.

```text
depctl CLI / MCP (serve) / watcher
        ↓
   local daemon API (HTTP/JSON over a Unix socket)
        ↓
   single store owner (depctl daemon run)
        ↓
 bbolt / Badger / vector
```

1. **The process:** `depctl daemon run`, foreground and long-running, plus `depctl daemon status` and `depctl daemon stop`.
2. **Local-only transport:** HTTP/JSON over a Unix domain socket at `<dir of control.db>/depctld.sock`, mode 0600. No TCP listener and no auth; file permissions are the access control. Long-running operations (sync, GC) stream NDJSON progress lines so the CLI can print output as it happens.
3. **Ownership is proven by the bbolt lock, not by the socket file.** At startup the daemon opens `control.db` first. Success means no other daemon is alive, so any socket file left behind by a crash is stale and is removed before binding. A socket file is never treated as evidence that a daemon is running.
4. **Startup, precisely:**
   ```text
   1. attempt exclusive store ownership (open control.db)
   2. if ownership fails:
        dial the existing socket
        - healthy      → "already running (pid N)", exit
        - no response  → ownership error: control.db is locked but nothing
                         answers on <socket> (some other depctl process holds it)
   3. once owned: remove any stale socket → bind → serve
   ```
5. **Store-touching commands require the daemon, and start one if it's absent.** A client that finds no daemon spawns `depctl daemon run` detached, polls the socket for up to 5 seconds, and continues. `daemon.autostart: false` opts out, and commands then fail with an actionable "daemon is not running" error.
   - **No command ever falls back to opening the stores directly.** That fallback is what would re-create the contention this ADR removes, so each command has exactly one path to state.
   - Exceptions, which never talk to the daemon: `init` (creates the stores, and refuses while a daemon is running), `config validate`, and `registry`. `doctor` is a partial exception: it reports "daemon not running" as a check result instead of failing, since diagnosing a stopped daemon is its job.
   - Auto-start matters operationally, not just for convenience. Without it, this ADR would replace "the user must keep `depctl watch` running" with "the user must keep `depctl daemon run` running, and now *everything* depends on it" — architecturally better, operationally worse.
   - **Simultaneous auto-starts need no coordination.** Both spawned daemons race for the bbolt lock; the loser finds the store owned, reports "already running", and exits; both clients connect to the winner. This is the same rule as §3, which is why no startup lockfile, pidfile, or spawn mutex exists.
6. **Syncs are serialized globally for v1.** Generations are keyed by dependency and shared across projects, so two projects that reference the same dependency version could otherwise race to build the same generation, and bbolt allows only one writer anyway. Per-project state is tracked separately from the global run lock, so allowing cross-project concurrency later means removing the lock, not redesigning the scheduler.
7. **Sync requests collapse.** Per project the daemon tracks `syncing` plus a `dirty` flag: a request that arrives mid-sync sets `dirty`, and at most one follow-up sync runs when the current one finishes. Repeated filesystem events therefore cost one extra sync, not one per event. The queue is in memory only — no bbolt jobs, no durable queue, no worker framework. Pending work lost to a daemon restart is recovered by the next file change or command.
8. **`depctl serve` becomes a stdio MCP proxy.** It opens no store and builds no embedder. `internal/mcp` keeps the tool definitions and result formatting and depends on a consumer-side interface that both `query.Service` (in the daemon) and the daemon client satisfy, so query behavior has exactly one implementation and is not rebuilt in the transport layer.
9. **Watch stops being its own process.** `internal/watch` is reused unchanged and feeds the daemon's scheduler. `depctl watch` survives only as a deprecated shim that ensures the daemon is running, says watching is daemon-managed, and exits.

**Scope discipline.** This is a process-model migration, not a rewrite. The goal is the smallest implementation that establishes single store ownership *without redesigning domain behavior*:

```text
existing operation        existing CLI
      ↓                        ↓
daemon handler            daemon client
      ↓
existing implementation
```

Storage, sync, query, resolver, MCP, watch, and GC logic stay where they are. Explicitly not built: a distributed scheduler, Redis, Kafka, a durable job queue, TCP networking, auth, a web UI, launchd/systemd installation, multi-host support, a generalized workflow engine.

## Consequences

**Positive:**
- No lock contention. Any number of agent sessions can run `depctl serve` at once, and CLI commands work while they do.
- Watching syncs immediately instead of retrying every 30 seconds behind a connected agent.
- One sync scheduler for every trigger — file change, CLI, and MCP — so syncs can't overlap or duplicate.
- The daemon holds the stores open, so per-command Badger open cost disappears from the hot path.

**Negative / tradeoffs:**
- A background process now exists that the user never explicitly started, and it stays running until `depctl daemon stop` or a reboot. `depctl daemon status` shows it; `depctl daemon run` in the foreground is the debug path.
- The daemon inherits the environment (PATH, HOME) of whichever command auto-started it, and that's the environment its resolvers use. A daemon first started by an agent's `depctl serve` inherits the agent's PATH.
- A daemon crash loses in-flight and pending sync work. The next command auto-starts a replacement.
- Command output for sync and GC is relayed over the socket rather than written directly by the process doing the work.
- Every store-touching command now has a network hop and a serialization boundary; tests need a daemon running.

## Related

- `docs/tickets/completed/19-watch-mode/` — WATCH-004 (this ADR) through WATCH-012 implement it.
- ADR-010 (native-first execution model) — amended: its "long-running `serve` daemon" is now `depctl daemon run`; `serve` is a stdio proxy. The native-first reasoning is unchanged and applies to the daemon, which runs resolvers against the user's real toolchain.
- ADR-006 (MCP as the primary agent interface) — unchanged: `internal/mcp` is still the only package importing the MCP SDK, and still contains no business logic.
- `internal/control/bbolt` `ErrLocked` (epic 18, OPS-001) — the lock error this ADR builds ownership detection on.

## Update (2026-10-07)

- **Auto-start within one process is now coordinated.** §5 says simultaneous auto-starts need no coordination. That still holds across processes (the bbolt lock decides), but within one process `spawnDaemonOnce` (`internal/cli/daemon.go`) now lets concurrent callers share a single spawn attempt, which lasts until the new daemon answers on its socket, and the parent reaps the child. Without this, a losing subprocess could stay blocked on the lock and later start an unrequested daemon.
- **The daemon owns more stores.** Besides `control.db` and Badger, the default embedded vector store (`vectors.db`, since v0.2.0) and the keyword index (`keyword.db`, since v0.3.0) are bbolt files in the same data directory, opened only by the daemon. "vector" in the diagram above can be one of those files or an external Qdrant, pgvector or Weaviate service.
