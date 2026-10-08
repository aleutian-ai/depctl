# MCP bootstrapping gap — fixed

**Status:** fixed. Kept as a record of the gap and the fix, since `internal/cli/daemon.go`'s `ensureInitialized` doc comment points here.

## The gap

Register `depctl` as an MCP server in an agent like opencode, point it at a repo that has never run `depctl init` or `depctl scan`, and two things broke:

1. `ensureDaemon` hard-refused with "run `depctl init` first" — there's no terminal in an MCP session to run that from, so `depctl serve` itself couldn't even start.
2. Even past that, an unscanned repo gave the agent zero context and no self-service way to fix it: no `scan` MCP tool existed, only `sync_project` (which needs a `project_id` that doesn't exist yet).

Concretely: run opencode with depctl registered as an MCP server on a repo right now, and it just has no context — not a hypothetical, the actual first-use experience.

## The fix (two pieces)

**Auto-init.** `runInit`'s body was extracted into `initStores(out io.Writer) error`. `ensureDaemon` no longer calls a hard-refusing `requireInitialized()`; it calls `ensureInitialized(ctx)`, which checks whether `control.db` exists and, if not, silently runs `initStores` (status lines to stderr) before spawning the daemon — the same "don't make the user do a manual step first" philosophy already applied to autostarting the daemon itself, extended one step earlier.

**`scan_project` MCP tool.** Mirrors `sync_project`'s shape (`internal/mcp/server.go`'s `ScanTrigger` interface, `internal/cli/query_client.go`'s `daemonScanTrigger` adapter calling `client.Resolve`), but unlike `sync_project` it has **no enable/disable gate** — it's always registered. Reasoning: it only writes project registration/resolution metadata (no clone, no vector backend writes), so it carries none of `sync_project`'s resource-cost surprise, and gating the one tool that gives a fresh session any context at all would defeat the point. Defaults to scanning the MCP server process's own working directory (the repo the agent is already in) when called with no arguments.

`toolError`'s `ErrProjectNotFound` message was updated to point at `scan_project` instead of a CLI command an agent session can't run.

## Verified

Live end-to-end (fresh `$HOME`, no prior `depctl init`/`scan`, `depctl serve` launched as a real subprocess with an MCP client over `CommandTransport`, cwd set to an unregistered repo):
`knowledge_status` → empty → `scan_project` (no args) → `knowledge_status` → shows the project registered with all its dependencies resolved. No human step, no error, no daemon left running afterward.
