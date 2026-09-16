# Epic: Watch Mode

Automatic local refresh: watch only dependency manifest files (never source code) and drive resolve → plan → sync when they change. Corresponds to Milestone 18 in the implementation plan.

WATCH-001..003 shipped `ragctl watch` as a foreground command. Real use showed it can't sync while an agent is connected: every stdio `ragctl serve` session holds `control.db`'s bbolt lock for its whole lifetime, and so does any other long-lived process. WATCH-004..011 replace direct multi-process store access with one long-lived local daemon that owns the stores. `serve`, watch, and every store-touching CLI command become its clients over a Unix socket. See ADR-011 (WATCH-004).

```text
ragctl CLI / MCP (serve) / watcher
        ↓
   local daemon API (HTTP/JSON over a Unix socket)
        ↓
   single store owner (ragctl daemon run)
        ↓
 bbolt / Badger / vector
```

## Tickets
- [WATCH-001](WATCH-001-manifest-watch-list.md) — Compute the minimal per-project set of manifest files to watch. *(done)*
- [WATCH-002](WATCH-002-fsnotify-watcher.md) — Debounced fsnotify watcher handling rename/recreate/removal, emitting change events. *(done)*
- [WATCH-003](WATCH-003-ragctl-watch.md) — `ragctl watch` foreground daemon: change event → resolve → plan → enqueue sync. *(done)*
- [WATCH-004](WATCH-004-single-owner-daemon-adr.md) — ADR-011: single-owner daemon with local RPC clients.
- [WATCH-005](WATCH-005-daemon-process-lifecycle.md) — `ragctl daemon run|status|stop`: store ownership, Unix socket, second-instance and stale-socket handling, graceful shutdown.
- [WATCH-006](WATCH-006-sync-scheduler.md) — In-memory per-project scheduler: `idle`/`syncing` + `dirty`, collapsed requests, one follow-up, global serialization.
- [WATCH-007](WATCH-007-watch-in-daemon.md) — Watch mode moves into the daemon (reusing `internal/watch`); no foreground watcher remains.
- [WATCH-008](WATCH-008-write-commands-via-daemon.md) — Resolve/sync/GC API; `scan`, `sync`, `gc` become daemon clients with streamed output.
- [WATCH-009](WATCH-009-read-commands-via-daemon.md) — Status/project/deps/plan/describe/doctor through the daemon.
- [WATCH-010](WATCH-010-serve-mcp-proxy.md) — `ragctl serve` becomes a stdio MCP proxy; no store access, same tools.
- [WATCH-011](WATCH-011-single-owner-invariant.md) — Enforce "only the daemon owns the stores" with tests; remove the old model; docs.
- [WATCH-012](WATCH-012-daemon-autostart.md) — Clients auto-start the daemon when it's absent (`daemon.autostart`, default true); `ragctl watch` becomes a deprecated shim.
- [WATCH-013](WATCH-013-mcp-progress-notifications.md) — `sync_project`/`scan_project` relay their streamed per-dependency output as MCP progress notifications, closing the "no feedback during a long sync" gap found during MCP-bootstrapping follow-up. *(done)*
- [WATCH-014](WATCH-014-embedding-readiness.md) — the daemon checks Ollama reachability and auto-pulls a missing embedding model in the background at startup, off any client's request path; `sync`/search fail fast with an actionable message instead of hanging while not ready. *(done)*
- [WATCH-015](WATCH-015-vector-backend-readiness.md) — the same background-readiness treatment for Qdrant: found live, right after WATCH-014 shipped, running the real MCP-first scenario end to end — a raw `dial tcp 127.0.0.1:6333: connect: connection refused` was still reaching the agent, once per dependency, because nothing checked Qdrant reachability before `sync`/GC/search tried to use it. *(done)*
- [WATCH-016](WATCH-016-managed-qdrant-bootstrap.md) — extends WATCH-015: when Qdrant is unreachable and `vector.managed: true` (the new-install default), the daemon starts its own pinned, loopback-only, persistently-stored Qdrant container via podman or docker — Qdrant stays a hard requirement, this just makes satisfying it operationally invisible when possible. Never containerizes the daemon itself. *(done)*
- [WATCH-017](WATCH-017-doctor-backend-readiness-visibility.md) — `ragctl doctor` gains granular embedding-backend/vector-backend checks surfacing WATCH-014/015/016's state, consistent in wording with what `sync`/search themselves report. *(done)*
- [WATCH-018](WATCH-018-sync-project-bounded-wait.md) — `sync_project` never blocks an MCP client past a small bound, regardless of how long a first sync of a large dependency legitimately takes; a still-running response tells the agent to check back instead of assuming failure. Found live: opencode's own tool-call timeout gave up on a real sync (`cloudflare/circl`) that was in fact succeeding server-side the whole time — the agent had no way to know that on its own. *(done)*

- [WATCH-019](WATCH-019-jit-sync-on-search.md) — `search_dependency_docs` against an unsynced-but-resolvable dependency triggers a sync scoped to just that dependency and retries, instead of requiring a separate, whole-project `sync_project` call first. The underlying mechanism (`SyncOptions.Dependency`) already existed end to end — just never exposed over MCP. *(done)*
- [WATCH-020](WATCH-020-sync-priority-preemption.md) — WATCH-019's JIT sync, when a background whole-project sync is already running for the same project, jumps that dependency to the front of the in-progress run's remaining queue instead of queuing a fully redundant second run behind it (the daemon serializes all sync work globally, one at a time). *(done)*

## Build order
004 → 005 → **012** → 006 → then 007, 008, 009 in any order (all need 005; 007/008 need 006) → 010 (needs 008's sync endpoint) → 011 → 013 → 014 → 015 → 016 → 017 → 018 → 019 → 020.

012 is numbered last but built early, right after the daemon exists, so every client command uses `ensureDaemon` from the start.

## Non-goals (whole epic)
Distributed scheduler, Redis, Kafka, a durable job queue, TCP networking, auth, a web UI, launchd/systemd installation, multi-host support, a generalized workflow engine. Don't redesign storage, query, resolver, or sync logic. This is a process-model migration, not a rewrite: the goal is the smallest implementation that establishes single store ownership without redesigning domain behavior.

```text
existing operation        existing CLI
      ↓                        ↓
daemon handler            daemon client
      ↓
existing implementation
```
