# WATCH-010: `ragctl serve` as a stdio MCP proxy

**Epic:** Watch Mode
**Status:** planned
**Depends on:** WATCH-008
**Estimated size:** medium

## Goal
`ragctl serve` stays the command agents launch over stdio, with the same tool names, inputs, and outputs. It stops opening any persistent store: every tool call is translated into a daemon API call, so any number of agent sessions can connect at once without lock contention.

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

Daemon connection drop mid-session: tool calls return an MCP tool error ("ragctl daemon is not reachable") rather than crashing `serve`. The proxy doesn't try to reconnect by itself; the next call dials again.

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
- [ ] `ragctl serve` opens no persistent store and builds no embedder or backend.
- [ ] Every MCP tool behaves exactly as before, with the same names.
- [ ] The query/search API operation exists; the MCP query path goes through the daemon.
- [ ] Multiple simultaneous `serve` sessions work.
- [ ] ADR-006's Related section, `docs/internal/mcp.md`, and architecture's `ragctl serve` flow updated.
