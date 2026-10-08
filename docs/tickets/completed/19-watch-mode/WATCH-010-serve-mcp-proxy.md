# WATCH-010: `depctl serve` as a stdio MCP proxy

**Epic:** Watch Mode
**Status:** done
**Depends on:** WATCH-008
**Estimated size:** medium

## Goal
`depctl serve` stays the command agents launch over stdio, with the same tool names, inputs, and outputs. It stops opening any persistent store: every tool call is translated into a daemon API call, so any number of agent sessions can connect at once without lock contention.

## Non-goals
- An MCP endpoint on the daemon itself, or HTTP MCP transport.
- Changing tool names, schemas, result formatting, or the security note.
- Changing `internal/query` logic.

## Simplicity constraints
- Tool definitions and formatting stay in `internal/mcp`, which remains the only package importing the MCP SDK (ADR-006). The only change there is that `Deps.Query` becomes a consumer-side interface instead of the concrete `*query.Service`.
- `query.Service` keeps running in the daemon, and the client implements the same interface over HTTP, so there's one query implementation.

## Design
In `internal/mcp`:
```go
// Querier is the query capability MCP tools need; *query.Service
// implements it in-process and the daemon client implements it over
// the socket.
type Querier interface {
    SearchKnowledge(ctx context.Context, q query.Query) (query.SearchResult, error)
    GetDependencyVersion(ctx context.Context, projectID, pkg string) (domain.DependencyVersion, error)
    GetProjectDependencies(ctx context.Context, projectID string) ([]query.ProjectDependency, error)
    GetReleaseChanges(ctx context.Context, dependency, from, to string) ([]query.ReleaseChange, error)
    Status(ctx context.Context) (query.Status, error)
}
```
Include only the methods `internal/mcp/tools.go` actually calls. Trim this list to match.

Daemon endpoints mirror the `Querier` methods one to one. They're JSON bodies of the existing `query` types, which already serialize, and they call the daemon's `query.Service`:
- `POST /v1/query/search`
- `GET /v1/query/dependency-version`
- `GET /v1/query/project-dependencies`
- `POST /v1/query/release-changes`
- `GET /v1/query/status`

`sync_project`'s `mcp.SyncTrigger` is implemented by the client over WATCH-008's sync endpoint, so an agent-triggered sync goes through the scheduler too. `server.mcp.enable_sync_tool` keeps gating it.
- **Ownership of that config setting:** the daemon owns the config, so `serve` reads `server.mcp.enabled` and `enable_sync_tool` from `GET /v1/health`, not from its own config load. Agent and daemon then can't disagree.

`runServe`:
```text
dial daemon (ErrNotRunning → print to stderr and exit 1)
mcp.New(mcp.Deps{Query: client, Sync: client, EnableSyncTool: health.EnableSyncTool})
Run over StdioTransport
```
There are no store, embedder, or vector-backend builders in `serve` any more; the daemon does embedding.

Daemon connection drop mid-session: tool calls return an MCP tool error ("depctl daemon is not reachable") rather than crashing `serve`. The proxy doesn't try to reconnect by itself; the next call dials again.

## Inputs / Outputs
- Input: MCP JSON-RPC over stdio (unchanged).
- Output: identical tool results.

## Failure behavior
- No daemon at startup: an error on stderr, exit 1. The agent's MCP list shows the server as failed, with a message naming the fix.
- A daemon error on a call surfaces as the same tool error today's in-process path would give (not-found, disabled, and so on).

## Tests
- `internal/mcp/offline_test.go` and `tools_test.go` keep passing unchanged with `*query.Service` as the `Querier`, which proves the interface is a pure refactor.
- New test: a real daemon on a temp socket plus `serve`'s server wired to the daemon client, connected with the SDK's in-memory client transport. `search_dependency_docs` and `get_dependency_version` return the same results as the in-process path on the same fixture (the MCP-004 fixture).
- Two `serve` instances connected at once both answer (previously the second would fail with `ErrLocked`).
- `sync_project` through the proxy triggers a scheduler run, and is refused when `enable_sync_tool` is false.
- `serve` with no daemon exits 1 with the message, and creates no `control.db`.

## Acceptance criteria
- [x] `depctl serve` opens no persistent store and builds no embedder or backend.
- [x] Every MCP tool behaves exactly as before, with the same names.
- [x] The query/search API operation exists; the MCP query path goes through the daemon.
- [x] Multiple simultaneous `serve` sessions work (they always shared one daemon via `ensureDaemon`/`spawnDaemonOnce`; this ticket just made `serve` itself join that path).
- [x] `docs/internal/daemon.md` and `docs/internal/cli.md` updated. (`docs/internal/mcp.md` and ADR-006 not touched — see note below.)

## Post-implementation notes

Built to this design's intent, with a few deliberate shape differences worth recording:

- **Interface named `QueryService`, not `Querier`** (`internal/mcp/server.go`) — same five methods this ticket specifies (trimmed to what `tools.go` actually calls, `GetProvenance` excluded since nothing calls it).
- **Routes are flat, all-POST JSON, not `/v1/query/...` with mixed GET/POST** (`/v1/search`, `/v1/project-dependencies`, `/v1/dependency-version`, `/v1/release-changes`, `/v1/knowledge/status`) — matches every other existing daemon route's convention (`/v1/sync`, `/v1/gc`, `/v1/projects/resolve`), rather than introducing a new REST-ish shape for just this one ticket. `/v1/knowledge/status` specifically avoids colliding with the pre-existing `/v1/status` (daemon/process health — an unrelated concept).
- **The query.Service an MCP session searches against is split into two lazily-built halves**, not built once at daemon startup as this design's "the daemon does embedding" phrasing might suggest: `engine.baseQueryService()` (stores only, used by everything except search) and `engine.fullQueryService(ctx)` (adds the embedder/vector backend, memoized, built only on a session's first actual search). This wasn't in the original design — it exists so a daemon started for `scan`/`sync`/`gc` alone never has to reach a live embedder just to answer `knowledge_status`/`list_project_dependencies`/etc.
- **The `server.mcp.enabled`/`enable_sync_tool` daemon-authoritative check is implemented**, per this design's "ownership of that config setting" note — `runServe` still does a cheap local config check first (so a deliberately-disabled MCP server never pays the cost of auto-starting a daemon), but the actual gating value comes from `GET /v1/health` once connected.
- **Not updated:** `docs/internal/mcp.md` and ADR-006's Related section, since neither's actual content changed (the tool definitions, schemas, and MCP-SDK-only-import invariant ADR-006 describes are all still true; only `Deps.Query`'s type changed, already covered in `docs/internal/daemon.md`/`cli.md`).
- Tests: real-daemon integration tests (`internal/cli/query_client_test.go`) exercise `daemonQueryService`/`daemonSyncTrigger` against a genuinely separate `depctl daemon run` process, not an in-memory transport pair — the existing `internal/mcp` protocol-level tests (`offline_test.go`, `tools_test.go`) were confirmed to keep passing unchanged, proving the interface swap was a pure refactor as this design intended.
