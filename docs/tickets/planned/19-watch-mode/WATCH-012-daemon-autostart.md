# WATCH-012: Client daemon auto-start

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-005
**Estimated size:** small

## Goal
Every daemon-dependent command connects to the daemon, and if none is running it starts one: it spawns `ragctl daemon run` detached, waits a bounded time for the socket, and continues. The user doesn't have to keep a terminal open. After `ragctl init`, the daemon is effectively invisible. `ragctl daemon run` stays as the explicit foreground/debug mode.

Build this right after WATCH-005 (before 007..010, despite the number), so those tickets' clients use it from the start.

## Non-goals
- launchd/systemd units or any OS service installation.
- Daemon restart on crash (a crashed daemon is auto-started by the next command, which is enough).
- Stopping an idle daemon automatically (idle timeout). Revisit if a daemon left running proves to be a problem.

## Simplicity constraints
- **The bbolt lock is the only arbiter of which daemon wins** (ADR-011 §4). No startup lockfile, pidfile, or spawn mutex. Concurrent auto-starts are allowed; the losers exit.
- One function, `ensureDaemon(cfg) (*client.Client, error)` in `internal/cli`, used by every daemon-dependent command.

## Design
Config (`internal/config`):
```yaml
daemon:
  autostart: true   # default; false = "daemon not running" error, WATCH-005 behavior
```
`DaemonConfig{Autostart bool}` is added to `Config` and `Default`.
- **Missing key:** `Load` never applies defaults (see `docs/internal/config.md`), so a config written before this ticket has no `daemon` key. Treat a missing key as `true` by making the field a `*bool`. Treating it as `false` would silently turn auto-start off for every existing install.

`ensureDaemon`:
```text
1. client.Dial(socket) → connected: return
2. ErrNotRunning and daemon.autostart false → return the WATCH-005 "not running" error
3. spawn: exec os.Executable() "daemon" "run"
     - new session (SysProcAttr.Setsid), so it outlives the spawning command
     - stdin = /dev/null; stdout+stderr appended to <dir of control.db>/ragctld.log
       (never inherit the parent's stdio: when the parent is `ragctl serve`, its
        stdout is the MCP JSON-RPC stream)
     - Release() the process; don't wait on it
4. poll client.Dial every 100ms for up to 5s
5. connected → return
   timed out → error: "ragctl daemon did not start within 5s; last log lines:" + tail of ragctld.log
```

Concurrent auto-start (two commands at once) resolves through the lock:
```text
A and B both find no socket, both spawn a daemon
daemon A opens control.db → owns it → listens
daemon B: control.db open → ErrLocked → exits
both clients' polling connects to daemon A
```

WATCH-005's startup changes to make the losing side quiet and correct:
```text
1. open control.db
2. if ErrLocked:
     dial the socket
     - healthy → print "ragctl daemon already running (pid N)", exit 1
       (an auto-started loser writes this to ragctld.log; it's a normal race, not an error)
     - not answering → exit 1 with the ownership error: control.db is locked
       but no daemon answers on <socket>, so another ragctl process holds it
       (e.g. an older version of `ragctl serve`)
3. owned → remove any stale socket → bind → serve
```

Daemon startup must stay fast and must not depend on external services:
- Build the embedder and vector-backend clients at startup without contacting the services.
- Probe embedder dimensions lazily, on the first query or sync, and cache the result.

Otherwise `ragctl status` would fail whenever Ollama is down, and auto-start would time out waiting on a network probe.

`ragctl watch` (WATCH-007's shim) calls `ensureDaemon`, then prints:
```
watch is now managed by the ragctl daemon (pid N, socket <path>).
watching is on while watch.enabled is true; follow with `ragctl daemon status`.
```
and exits 0. It never runs a watcher itself. With `daemon.autostart: false` it prints the "not running" error, as every other client does.

**Environment:** the spawned daemon inherits the environment of the command that started it: HOME/XDG, so it resolves the same config and data paths, and PATH, which resolvers use to find `go` and friends. A daemon first auto-started by an agent's `ragctl serve` gets the agent's PATH. The daemon logs its PATH at startup, and resolver failures already name the missing executable. Known limitation, documented rather than solved; `ragctl daemon stop` then any command restarts it with the current shell's environment.

## Inputs / Outputs
- Input: any daemon-dependent command, with no daemon running.
- Output: a detached daemon process; `ragctld.log` beside `control.db`; the command proceeds normally.

## Failure behavior
- Not initialized (no `control.db`): reconciled by the MCP-bootstrapping fix (`docs/scratch/mcp-bootstrapping.md`) — `ensureDaemon` no longer refuses here. It calls `ensureInitialized`, which transparently runs the same idempotent work `ragctl init` does before spawning, so an MCP session with no terminal to run `ragctl init` from isn't stranded.
- Daemon fails to start (bad config, socket path too long): the client reports the timeout plus the log tail.
- `daemon.autostart: false`: the WATCH-005 error, unchanged.

## Tests
- Auto-start tests need a real `ragctl` binary, because `os.Executable()` is the test binary under `go test`.
  - Build `./cmd/ragctl` once per package (`TestMain` or a `sync.Once` helper) into a temp dir.
  - Point a package-level `daemonExecutable` override at it.
- Other CLI tests set `daemon.autostart: false` and start an in-process daemon through the shared helper, so they don't spawn processes.
- Specific tests:
  - No daemon: `ragctl status` auto-starts one, succeeds, and `daemon status` then shows it running. Clean up with `daemon stop`.
  - Concurrent auto-start: two `status` commands at once both succeed, exactly one daemon runs, and the loser's `ragctld.log` line says "already running".
  - `daemon.autostart: false`: status fails with "not running" and no process is spawned.
  - Config without a `daemon` key: auto-start is on.
  - A stale socket plus auto-start: the daemon recovers the socket and the command succeeds.
  - A daemon that can't start (invalid config written after init): the client error includes the log tail within ~5s.
  - The `ragctl watch` shim: ensures the daemon, prints the message, exits 0, and no watcher runs in the shim process.
  - Auto-start from `serve`: the spawned daemon writes nothing to `serve`'s stdout. The MCP session's first message is a valid JSON-RPC response.

## Acceptance criteria
- [ ] Every daemon-dependent command auto-starts the daemon when absent, by default.
- [ ] `daemon.autostart: false` restores the explicit "not running" behavior.
- [ ] Concurrent auto-starts resolve through the bbolt lock; the loser exits quietly with "already running".
- [ ] No command falls back to opening stores directly, with or without auto-start.
- [ ] `ragctl watch` is a shim that ensures the daemon and exits.
- [ ] No launchd/systemd code.
- [ ] `docs/internal/config.md` (`daemon.autostart`), `docs/internal/daemon.md`, and README quick-start updated. With auto-start, the quick-start needs no `daemon run` step.

## Post-implementation note
`ensureDaemon`/`spawnDaemon` (`internal/cli/daemon.go`) implement this. A real race was found and fixed after initial implementation: concurrent auto-start callers used to each spawn their own subprocess to race for the lock, and a loser could still be parked in the lock wait after the winner was told to shut down, then inherit the freed lock and start a second, unrequested daemon. `spawnDaemonOnce` (in-process single-flight) plus a fail-fast 200ms lock timeout for the daemon's own startup attempt close this.
