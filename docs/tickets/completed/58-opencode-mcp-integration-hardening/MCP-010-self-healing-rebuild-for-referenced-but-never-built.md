# MCP-010: `search_dependency_docs`/`sync_project` can't self-heal a "referenced but never built" dependency

**Epic:** OpenCode/MCP integration hardening
**Status:** done
**Depends on:** OPS-005 (the CLI-side `--rebuild` fix this ticket extends to MCP)
**Estimated size:** small

## Goal
An agent hitting OPS-005's "referenced but never built" failure mode (a version reference exists, but the generation behind it never actually completed) must be able to diagnose and fix it entirely through MCP — no human needed to run the CLI.

## Problem, reported live
A real user's coding agent, working entirely through MCP (`opencode` + `ragctl serve`), hit exactly OPS-005's shape for `google.golang.org/protobuf`:

- `list_project_dependencies`/`knowledge_status` showed it as a real, resolved dependency.
- `sync_project` (whole-project, then scoped to just this package) reported success with nothing to do — `done: 0, failed: 0` — the planner correctly saw an existing reference and NOOPed, exactly as OPS-004/005 describe.
- `search_dependency_docs` for it failed every time with `query.ErrNoActiveGeneration`.

The daemon's own structured log (OBS-001) confirmed it precisely:
```
error="query: no active generation for this version: google.golang.org/protobuf"
```

OPS-005 already shipped the fix — `ragctl sync --rebuild --dependency <name>` clears the stale reference/pointer and forces a genuine rebuild — but **`--rebuild` was never threaded through to MCP's `sync_project` tool**, `api.SyncRequest.Rebuild`/`SyncOptions.Rebuild` stopped at the CLI/daemon-HTTP boundary. An agent working purely through MCP had no path to the fix at all; a human had to intervene on the machine directly, defeating the point of pointing an agent at ragctl in the first place.

## Design
Two complementary changes, not just one — an explicit lever plus an automatic one, so an agent is never stuck even if it doesn't know this failure mode exists by name:

1. **`internal/mcp.SyncTrigger.SyncProject`** gains a `rebuild bool` parameter, threaded straight through `internal/cli`'s `daemonSyncTrigger` into the same `api.SyncRequest.Rebuild`/`SyncOptions.Rebuild` path `ragctl sync --rebuild` already uses — no new daemon-side plumbing needed, since OPS-004/005 already built it end to end for the CLI.
2. **`sync_project`'s own input** (`SyncProjectIn.Rebuild`) exposes this directly to an agent, with the field's `jsonschema` description spelling out exactly when to reach for it: a dependency shown as real/resolved by `list_project_dependencies` but failing search with "no synced knowledge for this version yet" is the signal, and setting `dependency` to the exact package name plus `rebuild: true` is the fix.
3. **Fully automatic self-healing in `search_dependency_docs`** — the actual point of this ticket, not just documentation: its existing WATCH-019 JIT-sync-on-miss path (a resolvable-but-unsynced dependency triggers a scoped sync and retries once) already almost covers this, except a plain sync of a "referenced but never built" dependency succeeds (`failed: 0`) while doing nothing, so the retry search fails identically. Extended: if the search *still* fails with `ErrNoActiveGeneration` after that plain retry, try once more with `rebuild: true` before ever surfacing an error — an agent gets the fix transparently, with zero extra tool calls, zero awareness that anything unusual happened.
4. `toolError`'s `ErrNoActiveGeneration` message was rewritten to name the exact remedy (`sync_project` with `rebuild: true`) for any path that still reaches it despite the automatic retry (e.g. `sync` disabled, or the rebuild attempt itself failing).

## Non-goals
- No change to OPS-005's own CLI/daemon-side fix — this ticket only extends its existing reach to MCP.
- No automatic rebuild retry on the WATCH-020 priority-bump path (an already-running background sync) — that scenario means a sync is genuinely in flight, not stuck; forcing a rebuild there would be premature.
- No change to `sync_project`'s whole-project (no `dependency` set) behavior — `rebuild` is meaningless there, matching `SyncOptions.Rebuild`'s own existing "ignored if Dependencies is empty" contract.

## Tests
- `TestSearchDependencyDocsHandlerSelfHealsWithRebuildOnPersistentMiss` (`internal/mcp/tools_test.go`) — the direct regression: a fake sync that succeeds-but-NOOPs on `rebuild: false` and only seeds content on `rebuild: true` proves the handler tries both, in the right order (`[false, true]`), before returning a result.
- `TestSyncProjectHandlerPassesRebuildThrough` — confirms the tool's own `Rebuild` input reaches `SyncTrigger.SyncProject`.
- Existing WATCH-019/020 tests (`TestSearchDependencyDocsHandlerTriggersJITSyncOnMissingGeneration`, `...SkipsJITSyncWhenDisabled`, `...ReportsOriginalErrorWhenJITSyncFails`) all still pass unchanged — the new rebuild retry only ever fires as an additional fallback, never replacing the existing plain-sync-first behavior.

## Acceptance criteria
- [x] `sync_project`'s MCP input can force a rebuild directly (`rebuild: true` + `dependency`), reaching the same fix `ragctl sync --rebuild` already provides.
- [x] `search_dependency_docs` self-heals automatically: a "referenced but never built" miss is retried with `rebuild: true` after a plain retry doesn't fix it, with no separate agent action required.
- [x] `go build`/`go vet`/`go test ./...` (native and `hack/test-linux.sh`) clean; new tests pass, verified against a fake trigger reproducing the exact real-world sequence a live user's session showed (NOOP plain sync, `ErrNoActiveGeneration`, then a working rebuild).

## Post-implementation note: a second real bug found live, same day, same user

Shipping the fix above and watching it run against real production traffic surfaced a genuine regression risk in the design itself, not just confirmation it worked. The daemon's own OBS-001 log from the user's very next session showed `google.golang.org/protobuf` — a real, first-time, large dependency (26,325 normalized objects, 14,917 chunks) — taking **131 seconds just to embed**. Tracing the log's timestamps (repeated `query failed` lines at an exact ~500ms cadence for over 90 seconds) matched `waitForDependencyGeneration`'s existing WATCH-020 polling precisely, revealing the real mechanism: the *first* `search_dependency_docs` call blocked synchronously on `SyncTrigger.SyncProject` for the full build duration — `client.Sync`'s own underlying HTTP timeout is 4 hours, so nothing bounded that wait — almost certainly exceeding the calling MCP client's own tool-call timeout. The agent then issued a second, independent call, which (correctly) found the first sync already in flight, bumped its priority, and polled it to completion — the *existing* WATCH-020 machinery worked exactly as designed and the request eventually succeeded, but only after a duplicate-request race that a large dependency's real build time made newly likely, and this ticket's own new `rebuild` fallback added a second unbounded call on top of the pre-existing (WATCH-019) one.

**Fix:** neither JIT-sync attempt (plain or rebuild) blocks directly on `SyncTrigger.SyncProject` anymore. `triggerAndAwaitSync` runs it in the background (`context.WithoutCancel`, so the real build keeps running to completion regardless of what this call does) and waits up to `jitSyncPriorityWaitBound` (the same 90s constant WATCH-020 already established for exactly this handler) before giving up on retrying the search itself. If the bound elapses, the handler doesn't return a bare, permanent-looking `ErrNoActiveGeneration` — it names the dependency, explains a sync is genuinely still running in the background, and points the agent at the existing `sync_progress` tool for a real median/p90/confidence time estimate (`BATCH-001`'s own `SyncEstimate` machinery) rather than inventing a second, duplicate estimation mechanism here.

New test: `TestSearchDependencyDocsHandlerReportsStillRunningRatherThanBlocking` uses the existing `blockingSyncTrigger` test double (never released within the test, simulating a real in-progress build) with a shortened `jitSyncPriorityWaitBound`, and asserts the handler returns within ~2 real seconds (not blocking on the unfinished sync) with a message naming `sync_progress`.
