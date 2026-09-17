# Daemon / MCP topology — resolved (WATCH-010)

**Status: the gap this doc describes is fixed.** `ragctl serve` now opens no store and builds no embedder — it's a thin daemon client (`ensureDaemon` + `internal/cli/query_client.go`'s `daemonQueryService`/`daemonSyncTrigger`) exactly like every other command, satisfying ADR-011 §8. See `docs/internal/daemon.md` and `docs/internal/cli.md` for the current, accurate shape. This doc is kept as-is below for the historical record of what the bug looked like and why it mattered — panels 1 and 2 describe a scenario that can no longer happen.

Companion to the "Daemon architecture" section of [`docs/architecture.md`](../architecture.md).

## 1. Two processes, no handshake

Neither side needs a terminal held open, but each assumes it's the only long-running `ragctl` process — nothing checks.

```mermaid
flowchart TB
    subgraph cli["Terminal command — e.g. `ragctl scan .`"]
        C1["ensureDaemon(ctx) dials ragctld.sock — nothing answers"]
        C2["spawnDaemonOnce spawns `ragctl daemon run`, detached<br/>stdout/stderr → ragctld.log"]
        C3["holds control.db + badger/ for as long as it runs —<br/>no idle shutdown, no restart on reboot"]
        C1 --> C2 --> C3
    end

    subgraph mcp["MCP client — Claude Desktop, Claude Code, opencode..."]
        M1["host process launches `ragctl serve` as a stdio child,<br/>keeps it alive for the session"]
        M2["runServe opens control.db / badger/ directly —<br/>never dials the socket, never heard of the other lane"]
        M3["lifecycle owned entirely by the MCP host, not by ragctl itself"]
        M1 --> M2 --> M3
    end
```

Both lanes can be alive at once. Only one of them can hold `control.db`'s lock.

## 2. What happens when both are up

An MCP session is already open when a terminal command tries to auto-start its own daemon.

```mermaid
flowchart TB
    A["MCP client already has `ragctl serve` open,<br/>holding control.db's exclusive lock"]
    B["You run `ragctl scan .` in a terminal —<br/>ensureDaemon finds nothing on the socket, spawns daemon run"]
    A --> L
    B --> L
    L["control.db — one process may hold this lock at a time"]
    L --> E["openControlStoreForDaemonRun fails in 200ms with ErrLocked<br/>↓<br/>ownershipError probes ragctld.sock to name the owner<br/>↓<br/>nothing answers there either, since serve never bound it<br/>↓<br/>'control.db is locked but no daemon answers on ragctld.sock;<br/>another ragctl process holds it'"]

    classDef err fill:#f7e5da,stroke:#b5451d,color:#7c2e11;
    class E err;
```

True, but it doesn't say which process — or that it's `serve`.

## 3. What ragctl notices without a restart

Three things you might change while a daemon is already running, and how long it takes to notice:

| You do | ragctl notices | When |
|---|---|---|
| Drop a new manifest into `<data-dir>/registry/*.yaml` | `loadRegistryForCLI` reloads it on the very next `plan`/`sync` call | **Immediate** |
| `ragctl scan` a brand-new project directory | Same open store handle; the watch loop's periodic refresh starts watching it live | **Immediate** |
| Edit `config.yaml` — backend endpoint, embedding model, `watch.debounce`, `daemon.autostart`, ... | `runDaemonRun` loads config once and hands it to `engine` for its whole life | **Restart required** (`ragctl daemon stop`) |

## 4. One entry point instead of two (proposed)

If MCP is meant to be primary, the stateful process shouldn't be the one only a terminal command can start.

```mermaid
flowchart LR
    subgraph today["Today — two owners"]
        T1["CLI commands call ensureDaemon"] --> T2["one auto-started daemon run"]
        T3["ragctl serve opens the stores itself —<br/>a second owner, no coordination"]
    end

    subgraph proposed["Proposed — one owner"]
        P1["an MCP tool call reaches runServe"] --> P2["runServe calls ensureDaemon(ctx) too —<br/>same path scan/plan/sync already use"]
        P2 --> P3["exactly one background daemon, reached over the socket either way —<br/>serve becomes a stdio↔daemon-API bridge, not a second store owner"]
    end
```
