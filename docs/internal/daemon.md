# internal/daemon

`internal/daemon` is the one process that owns ragctl's persistent stores while it runs (ADR-011). Every other `ragctl` process — CLI commands, `ragctl serve`, the watcher — reaches that state through this package's HTTP/JSON API over a Unix domain socket, never by opening bbolt or Badger itself. This package owns transport, scheduling, and lifecycle only; the work behind each route is done by an `Engine`, implemented in `internal/cli` over the stores the daemon holds open, so no domain logic moves here. `internal/daemon/client` is the other side of the same contract, used by `internal/cli`'s command implementations.

## Key types and functions

Server and lifecycle:
- `Server` / `New(opts Options)` / `Serve(ctx) error` — binds the socket, starts watching (if enabled), serves HTTP until `ctx` ends or `Shutdown()` is called, then waits for the scheduler to drain before returning — `internal/daemon/server.go`.
- `Engine` — the daemon's view of ragctl's actual functionality: `Status`, `Projects`, `ProjectIDs`, `Sync`, `Scan`, `Plan`, `GC`; the read-only MCP query surface `Search`/`ProjectDependencies`/`DependencyVersion`/`ReleaseChanges`/`KnowledgeStatus`; `ProjectList`/`ProjectGet` (`project`/`deps`); `Describe`; and `Doctor` (every check that needs the stores — the two PATH-only checks stay client-side regardless, see `docs/internal/cli.md`). Defined consumer-side here and implemented in `internal/cli/daemon.go`'s `engine` struct over the already-open stores — `internal/daemon/server.go`.
- `Options` — `Engine`, `Socket`, `ControlPath`, `Version`, `WatchEnabled`, `Debounce`, `MCPEnabled`, `EnableSyncTool`, `Logf`, `Out` — `internal/daemon/server.go`.
- `Shutdown()` — asks a running `Serve` to stop; returns immediately, closing `s.stop` — `internal/daemon/server.go`.

Scheduling (`internal/daemon/scheduler.go`):
- `Scheduler` / `NewScheduler(base, run SyncFunc, runGC GCFunc, logf)` — runs sync one project at a time and GC daemon-wide, sharing one `global` lock between them so neither ever runs while the other is (GC decides a generation is unreferenced by reading the same `references` state a concurrent sync can be actively changing — a correctness requirement, not just v1 simplicity; see `docs/scratch/action-controller-proposal.md`).
- `Request(projectID, opts, out) <-chan Result` / `RequestGC(dryRun, out) <-chan GCOutcome` — queue work and get back a channel for the run that covers it: the run it starts, or the single follow-up run if one of that kind is already in progress. A request that arrives mid-run collapses into that follow-up rather than queuing its own — `Force`/`Resolve` escalate (`||`) when collapsed, `Offline`/GC's `DryRun` de-escalate (`&&`/`||` respectively — DryRun specifically uses OR so a caller's explicit preview request can never be silently upgraded into a real delete just because it collapsed with someone else's).
- `LockProject(projectID) func()` — a separate, independent lock scan holds around one project's persist step, so two concurrent scans that discover the same project don't race `PutProject`/`PutResolution`'s read-modify-write. Refcounted: an entry is removed once nothing holds or wants it, so the map doesn't grow by one entry per project ever scanned.
- `maxActionDuration` (var, 30 min) — every sync and GC run is bounded by this, via `context.WithTimeout(context.WithoutCancel(base), maxActionDuration)`: survives `Shutdown` (an in-flight run finishes rather than leaving a half-built generation) but can't run forever, so one hung network call can't wedge the global lock — and therefore every future sync or GC — shut permanently.
- `States() map[string]string` — each project's `idle`/`syncing`/`syncing+dirty` state, for `ragctl status`-adjacent reporting.
- `Wait()` — blocks until no sync or GC run is in flight; `Server.Serve` calls this during shutdown, after the HTTP listener is already closed.

HTTP surface (`internal/daemon/handlers.go`, `internal/daemon/server.go`):
- Routes: `GET /v1/health`, `GET /v1/status`, `POST /v1/shutdown`, `POST /v1/projects/resolve`, `POST /v1/plan`, `POST /v1/sync`, `POST /v1/gc`, the read-only query routes `POST /v1/search`, `POST /v1/project-dependencies`, `POST /v1/dependency-version`, `POST /v1/release-changes`, `POST /v1/knowledge/status` (named to avoid colliding with `/v1/status`, which means daemon/process health, not query.Service's fleet-wide sync-coverage summary), and `POST /v1/projects/list`, `POST /v1/projects/get`, `POST /v1/describe`, `POST /v1/doctor` — path constants and every request/response type live in `internal/daemon/api`, shared by server and client so they can't drift.
- `handleResolve` — runs `Engine.Scan`, passing `s.scheduler.LockProject` as the per-project lock callback, then refreshes the watched set immediately (rather than waiting up to a minute for the periodic refresh) so a newly registered project starts being watched right away.
- `handleSync` / `handleGC` — go through `Scheduler.Request`/`RequestGC`; both stream NDJSON progress lines (`stream`, `internal/daemon/handlers.go`) so the CLI can print output as it happens instead of only at the end.
- `handleStatus` — calls `Engine.Status` then sets `GCRunning` from `gcBusy()`, since `Engine` has no scheduler awareness (deliberately — see the package doc on why domain logic stays in `internal/cli`).

Watching (`internal/daemon/watch.go`):
- `startWatch(ctx)` — wraps `internal/watch.Watcher`, run inside the daemon instead of as its own process (ADR-011 §9; `ragctl watch` survives only as a deprecated shim).
- `watchLoop(ctx)` — turns debounced `ChangeEvent`s into `Scheduler.Request(projectID, SyncOptions{Resolve: true}, ...)` calls; doesn't wait for the sync, since the scheduler already collapses repeats.
- `refreshLoop(ctx)` — re-reads the registered project list every `projectRefreshInterval` (1 minute) so projects added or removed while the daemon runs are picked up without a restart; also runs immediately after any request that changes the project set (`refreshProjects`, called from `handleResolve`).

Client (`internal/daemon/client/client.go`):
- `Client` / `New(socket)` / `Dial(ctx, socket) (*Client, error)` — `Dial` returns a `Client` only if a daemon actually answers `Health`, otherwise `ErrNotRunning`; `New` doesn't contact the socket.
- `Health`, `Status`, `Shutdown`, `Resolve`, `Sync`, `Plan`, `GC`, `Search`, `ProjectDependencies`, `DependencyVersion`, `ReleaseChanges`, `KnowledgeStatus`, `ProjectList`, `ProjectGet`, `Describe`, `Doctor` — one method per route, matching the server's handlers. All return `api.*` wire types (`Describe`'s exception: it decodes into a caller-supplied `any`, the same pattern `Plan` uses, since `Report` is a pre-existing `internal/cli` type not worth duplicating into `api`); converting `api.*` back to `internal/query`'s types is `internal/cli`'s job (`daemonQueryService`/`daemonSyncTrigger` in `internal/cli/query_client.go`), not this package's — a client stays a thin protocol library.
- **Every method wraps its own request with a timeout** (`defaultRequestTimeout` 90s, `describeRequestTimeout` 10min, `longRunningRequestTimeout` 35min for the streamed Resolve/Sync/GC calls) — `c.http` itself sets none, same as `http.DefaultClient`, the exact gap already found and fixed one layer down in the qdrant client. Without this, a stuck daemon-side handler would hang the *calling command* forever (not the whole daemon — Go's net/http runs each request on its own goroutine, so this was never global paralysis, but a real per-command risk). vars, not consts, so tests can shorten them — see `TestHealthTimesOutOnAStuckDaemon`.
- `api.Health.ConfigFingerprint` — `config.Config.Fingerprint()` (hashes the canonical YAML encoding, not the on-disk bytes, so cosmetic edits don't register) as of when the daemon started. Since config is loaded once for the daemon's whole lifetime, every `ensureDaemon` call compares this against a fresh load and warns on stderr if they differ (`warnIfConfigStale`), `ragctl daemon status` shows a `config:` line (`configFreshnessLabel`), and `ragctl doctor` gets a `config matches running daemon` check either way.
- `ErrNotRunning` — the socket file is absent, or stale and refuses connections; `internal/cli`'s `ensureDaemon` treats this as "try to auto-start."

## Dataflow

```mermaid
flowchart TD
    subgraph clients["internal/cli (ensureDaemon)"]
        Scan["runScan"]
        Sync["runSync"]
        GC["runGC"]
        Status["runStatus"]
        Serve["runServe\n(daemonQueryService,\ndaemonSyncTrigger)"]
        Watch["watched externally now\n(ragctl watch is a deprecated shim)"]
    end

    Scan -->|Resolve| Client["daemon/client.Client"]
    Sync -->|Sync| Client
    GC -->|GC| Client
    Status -->|Status| Client
    Serve -->|Search, ProjectDependencies,\nDependencyVersion, ReleaseChanges,\nKnowledgeStatus, Sync| Client
    Client -->|HTTP/JSON over\nUnix socket| HTTP["Server.routes()"]

    HTTP -->|/v1/projects/resolve| HResolve["handleResolve"]
    HTTP -->|/v1/sync| HSync["handleSync"]
    HTTP -->|/v1/gc| HGC["handleGC"]
    HTTP -->|/v1/status| HStatus["handleStatus"]
    HTTP -->|/v1/search,\n/v1/project-dependencies,\n/v1/dependency-version,\n/v1/release-changes,\n/v1/knowledge/status| HQuery["handleSearch, etc."]

    HResolve -->|Engine.Scan +\nscheduler.LockProject| Engine["Engine\n(internal/cli's *engine)"]
    HSync -->|Scheduler.Request| Sched["Scheduler\n(global lock, per-project collapsing)"]
    HGC -->|Scheduler.RequestGC| Sched
    Sched -->|Engine.Sync / Engine.GC| Engine
    HStatus -->|Engine.Status + gcBusy| Engine
    HQuery -->|Engine.Search etc,\nvia baseQueryService/fullQueryService| Engine

    FSWatch[("fsnotify\n(internal/watch)")] -->|debounced ChangeEvent| WatchLoop["watchLoop"]
    WatchLoop -->|Request Resolve:true| Sched

    Engine -->|already-open handles| Stores[("bbolt.Store + badger.Store\n(opened once, in runDaemonRun)")]
```

## Walkthrough

Scenario: no daemon is running. A user runs `ragctl scan .` in a Go module directory that's never been scanned before.

1. `runScan` (`internal/cli/scan.go`) resolves the path to absolute, then calls `ensureDaemon(cmd.Context())` (`internal/cli/daemon.go`).
2. `ensureDaemon` calls `socketPath()`, then `client.Dial(ctx, socket)`. Nothing is listening, so `Dial`'s `Health` call fails and returns `ErrNotRunning`.
3. `ensureDaemon` checks `cfg.Daemon.AutostartEnabled()` (true by default), then calls `ensureInitialized(ctx)`: if `control.db` doesn't exist yet, this transparently runs the same work `ragctl init` does (`initStores`, status lines to stderr) before proceeding — no hard "run `ragctl init` first" refusal, since an MCP session has no terminal to run that from (see `docs/scratch/mcp-bootstrapping.md`). Once the stores exist (already, or freshly created), `ensureDaemon` calls `spawnDaemonOnce(socket)`.
4. `spawnDaemonOnce` is the first caller for this socket, so it actually calls `spawnDaemon()`: `exec.Command(exe, "daemon", "run")`, detached, stdout/stderr redirected to `ragctld.log`, `cmd.Process.Release()`'d. (A second concurrent caller in the same process would instead wait on this same attempt's result via a shared channel — see `internal/cli/daemon.go`'s `spawnAttempt`.)
5. The new `ragctl daemon run` process (`runDaemonRun`, `internal/cli/daemon.go`) opens `control.db` via `openControlStoreForDaemonRun` — a 200ms lock timeout, deliberately shorter than the general 2s default, so a losing candidate in a concurrent auto-start race fails fast rather than parking inside the lock wait. It wins (nothing else was running), opens Badger, and calls `daemon.New(Options{Engine: &engine{...}, ...}).Serve(ctx)`.
6. `Serve` removes any stale socket file, binds `ragctld.sock`, starts watching (`startWatch`, since `watch.enabled` defaults true), and begins serving HTTP.
7. Back in the original CLI process, `ensureDaemon`'s `waitForDaemon` polls the socket until `Dial` succeeds, then returns the connected `*client.Client`.
8. `runScan` calls `c.Resolve(ctx, "/abs/path", cmd.OutOrStdout())` — a `POST /v1/projects/resolve` request, streamed NDJSON.
9. `handleResolve` calls `Engine.Scan(ctx, root, out, s.scheduler.LockProject)`. `engine.Scan` runs `scanAndResolve` (`internal/cli/scan.go`) against the store the daemon already has open — no second store handle, no lock contention with anything else, since this is the only process touching `control.db`.
10. `scanAndResolve` discovers the one project, acquires its `LockProject` lock (uncontended, since nothing else is scanning it), writes `PutProject`/`PutResolution`, releases the lock.
11. `handleResolve` calls `s.refreshProjects(ctx)` immediately after, so the new project starts being watched without waiting up to a minute for the periodic refresh.
12. The daemon streams progress lines back over the socket as it goes; `runScan`'s output looks identical to what it printed before ADR-011, even though the actual work now happens in a separate, persistent process.
13. The CLI process exits. The daemon keeps running — nobody told it to stop, and there's no idle timeout. It'll serve the next command — from this terminal, another terminal, or an MCP session's `ragctl serve` (now a client of the same daemon, ADR-011 §8) — without paying Badger's open cost again.

## Notes

- **Ownership is proven by the bbolt lock, not the socket file.** A socket left behind by a crash is stale and gets removed before a new daemon binds; a socket file is never itself evidence a daemon is running (ADR-011 §3).
- **Every store-touching command goes through the daemon now, `doctor` aside.** `doctor` dials without autostarting and falls back to opening the stores itself only if nothing answers — deliberate, since diagnosing a stopped or broken daemon is its job (see `docs/internal/cli.md`'s notes). `init`, `config validate`, and `registry` never talk to the daemon at all, since they don't need the stores it owns (or, for `init`, actively refuse to run while a daemon holds them).
- **`ragctl serve` follows this pattern now (ADR-011 §8, WATCH-010).** It opens no store and builds no embedder — `runServe` calls `ensureDaemon` like every other command, then wires MCP tools to `internal/cli/query_client.go`'s `daemonQueryService`/`daemonSyncTrigger`, which call the query routes above and the existing `/v1/sync` route respectively. `internal/mcp`'s `Deps.Query` is a consumer-side interface (`mcp.QueryService`, the five methods `tools.go` actually calls — not `GetProvenance`, which nothing calls yet) satisfied by either the real `*query.Service` (daemon-side) or this HTTP-backed wrapper. The `query.Service` an MCP session searches against is built lazily inside `engine`, split into a stores-only `baseQueryService` (used by everything except search — no embedder needed) and a `fullQueryService` that probes the embedder's dimensions only on a session's first actual search, so a daemon started for `scan`/`sync`/`gc` alone never has to reach a live embedder to start.
- **Config is loaded once, at daemon startup, and held for the daemon's whole lifetime** (`runDaemonRun` → `engine.cfg`). Editing `config.yaml` while a daemon is running (backend endpoint, embedding model, `watch.debounce`, `daemon.autostart`, ...) still has no effect until `ragctl daemon stop` and the next auto-start — that limitation itself isn't fixed. What changed: it's no longer silent. `ConfigFingerprint` (above) makes the drift detectable, and `ensureDaemon`, `daemon status`, and `doctor` all surface it. The registry (`<data-dir>/registry/*.yaml`) isn't subject to any of this — `loadRegistryForCLI` is called fresh on every `plan`/`sync`, not cached.
- **The scheduler's queue is in-memory only.** No bbolt jobs, no durable queue, no worker framework (ADR-011 §7, deliberately). Work queued but not yet started when the daemon stops is simply lost — nothing was written for it, so there's no corruption risk, just lost work the next file change or command re-requests.
- **Scan isn't tracked by `Scheduler.Wait()`.** `handleResolve` runs `Engine.Scan` synchronously in the HTTP handler goroutine, not through `Scheduler.start`/`inFlight`. Daemon shutdown still drains it, but via `http.Server.Shutdown`'s own connection-draining (bounded by `shutdownGrace`, 30s) rather than the scheduler's explicit wait — a different, less visible mechanism than what sync/GC get.
- **Scan-vs-sync of the same project isn't cross-locked** — only scan-vs-scan is (via `LockProject`). Deliberately left that way: bbolt's own transactional atomicity means the actual exposure is a sync computing its plan from a resolution snapshot a few milliseconds stale, not corruption — see `docs/scratch/action-controller-proposal.md`'s "Implementation notes" for the full reasoning and what would change if this ever needs tightening.
