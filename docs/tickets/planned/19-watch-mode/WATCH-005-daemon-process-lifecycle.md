# WATCH-005: Daemon process and lifecycle

**Epic:** Watch Mode
**Status:** planned
**Depends on:** WATCH-004
**Estimated size:** medium

## Goal
Add `ragctl daemon run|status|stop`: a long-running process that owns the persistent stores for its whole lifetime and serves a local HTTP/JSON API over a Unix socket. It also adds the shared API types and the client package every later ticket builds on. After this ticket the API has only health/status and shutdown; WATCH-008..010 add the rest.

## Non-goals
- Sync, watch, query, or GC endpoints (WATCH-006..010).
- Background/detached mode, auto-start, launchd/systemd units.
- TCP, auth, Windows named pipes.

## Simplicity constraints
- stdlib `net/http` over `net.Listen("unix", path)`. No RPC framework, no gRPC, no router library.
- One API version prefix (`/v1/`). Request/response structs live in `internal/daemon/api` and are shared by the server and client, so the two can't drift.
- No pidfile. Liveness comes from dialing the socket; ownership comes from the bbolt lock (ADR-011 §4).

## Design
Packages:
```text
internal/daemon/        Server: listener, lifecycle, handlers
internal/daemon/api/    request/response types, route constants
internal/daemon/client/ Client: http.Client with a unix-socket DialContext
internal/cli/daemon.go  `ragctl daemon run|status|stop`
```

Store access comes through a consumer-side interface, so `internal/daemon` never imports `internal/cli`. That would be an import cycle, because `RunSync`, `resyncProject`, and the GC and status builders live in `internal/cli`.
```go
// internal/daemon — grows one method per ticket that needs it.
type Engine interface {
    Status(ctx context.Context) (api.Status, error)
}
```
`internal/cli` implements `Engine` over the stores that `daemon run` opened, the same pattern `syncTrigger` already uses for `mcp.SyncTrigger`. Moving `RunSync` and the rest into a new package is out of scope.

Startup (`ragctl daemon run`):
```text
1. load config
2. open control.db (bbolt lock; ErrLocked → "a ragctl daemon (or another ragctl process) already owns <path>")
3. open Badger, build embedder + vector backend (same builders serve uses today)
4. socket = <dir of control.db>/ragctld.sock
   if it exists: remove it — step 2 proved no live daemon owns this store
5. listen unix, chmod 0600, serve HTTP
6. print "ragctl daemon listening on <socket>"
```

Shutdown (SIGINT/SIGTERM or `POST /v1/shutdown`):
```text
stop accepting requests (http.Server.Shutdown) → let in-flight requests finish
→ close stores → remove socket → print "stopped" → exit 0
```
A second SIGINT exits immediately, matching `ragctl watch` today.

Endpoints in this ticket:
- `GET /v1/health` returns `{pid, started_at, socket, control_path, version}`.
- `POST /v1/shutdown` returns 202 and starts the same shutdown path as SIGTERM.

Client:
- `client.Dial(socketPath)` returns `ErrNotRunning` when the socket is missing or the connection is refused, so callers can print "ragctl daemon is not running — start it with `ragctl daemon run`".
- `client.SocketPath(cfg)` is the one place the path is derived.

CLI:
- `ragctl daemon status` prints the health fields, or "not running" with exit 1.
- `ragctl daemon stop` posts shutdown, then waits up to 30s for the socket to disappear.

## Inputs / Outputs
- Input: config (control/badger paths, embedding, vector).
- Output: a socket file for the daemon's lifetime; log lines on stdout.

## Failure behavior
- Second `daemon run` against the same store: fails within the 2s bbolt lock timeout with the "already owns" message and exit 1. It must not touch the running daemon's socket.
- Stale socket (the daemon was SIGKILLed): the next `daemon run` removes it and starts normally.
- A socket path over the platform's `sun_path` limit (104 bytes on macOS, 108 on Linux) fails at startup with the path in the error, not an opaque `bind: invalid argument`.

## Tests
- Daemon starts, `GET /v1/health` answers, and while it runs `bboltstore.Open` on the same path returns `ErrLocked`.
- A second `daemon run` fails cleanly, and the first keeps answering.
- Stale socket recovery: create a plain file at the socket path (or leave the socket of a killed listener), then start the daemon; it starts and answers.
- SIGTERM (real `syscall.Kill`, unix build tag): exits 0, socket removed, stores closed (a subsequent `bboltstore.Open` succeeds).
- `daemon stop` shuts a running daemon down; `daemon status` reports both states.
- Test note: `t.TempDir()` paths on macOS can exceed `sun_path`. Tests put the socket under a short `os.MkdirTemp("", "rd")` dir; the config's control path decides where the socket goes.

## Acceptance criteria
- [ ] `ragctl daemon run|status|stop` exist; `internal/daemon`, `internal/daemon/api`, and `internal/daemon/client` exist with package docs.
- [ ] The daemon holds `control.db` and Badger open for its lifetime; no other process can open them while it runs.
- [ ] Second instance, stale socket, SIGTERM, and `daemon stop` behave as specified, with tests.
- [ ] No TCP listener; socket mode 0600.
- [ ] `docs/architecture.md` and a new `docs/internal/daemon.md` (plus the `docs/internal/README.md` index) describe the process and its lifecycle.
