# WATCH-008: `scan`, `sync`, and `gc` through the daemon

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-006
**Estimated size:** medium

## Goal
Add the daemon's write operations (resolve project, sync project, GC) and convert `depctl scan`, `depctl sync`, and `depctl gc` into clients of them. Each command keeps its flags, output, and exit behavior. The daemon does the work.

## Non-goals
- Read-only commands (WATCH-009) and MCP (WATCH-010).
- Changing sync, plan, resolve, or GC logic. The handlers call the existing `RunSync`, `runScan`'s core, and the GC core.

## Simplicity constraints
- Long-running operations stream progress, since `sync` can take minutes (embedding). The response body is NDJSON: `{"log": "..."}` lines, then one `{"result": {...}}` or `{"error": "..."}` line. The CLI copies log lines to stdout as they arrive. No WebSockets, no SSE framework.
- A `lineWriter` bridges a handler's `io.Writer` to the NDJSON stream, so existing functions keep writing to an `io.Writer` unchanged.

## Design
`Engine` gains:
- `Scan(ctx, root string, out io.Writer) ([]string, error)`, which runs the current scan/resolve core and returns the affected project IDs;
- `GC(ctx, dryRun bool, out io.Writer) (api.GCResult, error)`.

Endpoints:
- **`POST /v1/projects/resolve`** `{root}`, streaming.
  - Runs `depctl scan`'s discover + resolve + store for `root`, then refreshes the watcher's project set.
  - Resolvers run in the daemon process. That's fine because the daemon is native and runs as the user (ADR-010), so the user's toolchain and credentials are the same.
- **`POST /v1/projects/{id}/sync`** `{dependency, offline, force}`, streaming.
  - Goes through `scheduler.Request` and waits on the returned channel.
  - A sync already running for that project means this request is satisfied by the follow-up (WATCH-006's merge rules). The CLI's log stream says so: "sync already running for <id>; queued a follow-up".
- **`POST /v1/sync`** `{offline, force}`, streaming.
  - `depctl sync` without `--project`. It requests every project and streams each result.
- **`POST /v1/gc`** `{dry_run}`, streaming.
  - Takes the scheduler's global run lock, so GC never interleaves with a sync.

The sync endpoints request with `Resolve: false`, so `depctl sync` keeps today's behavior of syncing the stored resolution without re-resolving. Only watch-triggered requests re-resolve (WATCH-006/007).

CLI:
- `runScan`, `runSync`, and `runGC` dial the daemon (`client.ErrNotRunning` gives the WATCH-005 message and exit 1), call the endpoint, print the stream, and map the result to today's summary line and exit code.
- `root` for scan is made absolute client-side, because the daemon's working directory isn't the user's.

## Inputs / Outputs
- Input: the same flags as today.
- Output: the same stdout text as today, now relayed from the daemon; the same exit codes.

## Failure behavior
- Daemon not running: clear message and exit 1. No fallback to opening stores.
- The client disconnects mid-sync (Ctrl-C on `depctl sync`): the sync keeps running in the daemon, because it's the scheduler's run, not the request's, and the daemon logs the result. The CLI prints "sync continues in the daemon" on interrupt.
- Resolve, sync, or GC error: returned as the stream's error line, and the CLI exits non-zero exactly as today.

## Tests
- `depctl sync --project <id>` against a running test daemon produces today's output and stores a promoted generation. Adapt an existing sync test to start a daemon first.
- `depctl scan <fixture>` registers the project through the daemon, and the daemon's watcher picks it up without a restart.
- `depctl sync` while a watch-triggered sync is running for the same project waits for and reports the follow-up.
- `depctl gc --dry-run` through the daemon matches today's output.
- Daemon not running: each command fails with the "not running" message.
- While the daemon runs, these commands succeed. Before this ticket they would have failed with `ErrLocked`, which proves they no longer open the stores.

## Acceptance criteria
- [ ] Resolve, sync, and GC API operations exist, with streamed progress.
- [ ] `depctl scan`, `depctl sync`, and `depctl gc` are daemon clients with unchanged flags, output, and exit codes.
- [ ] All syncs, CLI and watch alike, go through the one scheduler.
- [ ] Architecture and `docs/internal/cli.md` updated.

## Post-implementation note
`scan`/`sync`/`gc` are thin daemon clients (`internal/cli/scan.go`/`sync.go`/`gc.go` via `ensureDaemon`). `scan`'s persist step also gained a per-project lock (`Scheduler.LockProject`, refcounted so it doesn't grow unbounded) not originally specified here, closing a real race between two concurrent scans of the same project.
