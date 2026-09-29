# SCOPE-004: `prioritize_file` tool and `explain_call_site` JIT wiring

**Epic:** Ambient Sync and Scoped Priority
**Status:** done
**Depends on:** SCOPE-003 (needs the multi-dependency set, not a single string, to express "these N imports" precisely)
**Estimated size:** medium

## Goal
Close the two real gaps the tool-surface enumeration found: no tool prioritizes a whole file's imports at once, and `explain_call_site` — arguably the single most natural "the agent is working on this file" signal — has zero JIT-sync wiring at all today.

## Non-goals
- Go only, matching ragctl's established one-ecosystem-at-a-time convention. A non-Go file (or a Go file with unresolvable imports) degrades to "nothing matched, nothing to prioritize" — not an error.
- No directory/package-level version (enumeration item #4 — several related files prioritized together) — file-level is the concrete unit here; a directory-scoped version is a natural follow-up once this is proven, not assumed necessary yet.

## Design

### 1. New MCP tool: `prioritize_file`
- Input: `project_id`, `file` (path, relative to the project root or absolute — matching `explain_call_site`'s own existing convention).
- Parse the file's real import statements with `go/parser`/`go/ast` (the same machinery `internal/normalize/godoc` already uses to parse Go source — no new parsing approach introduced).
- Map each import path to the project's already-resolved dependency (`GetProjectDependencies`/the stored `Resolution` — already available, no new resolution step).
- For each matched dependency: if a sync is already running for this project, `BumpSyncPriority` for the whole set at once (SCOPE-003's multi-dependency `Dependencies []string`); if nothing is running, fire one scoped multi-dependency sync request covering exactly that set (not one request per import — avoids SCOPE-003's own coalescing scenario entirely by construction, since it's a single request from the start).
- Bounded response, mirroring `sync_project`'s own contract exactly (`mcpSyncWaitBound`, `StillRunning`/`ReportedSoFar`/`Total`/`Note`) — this tool must never block longer than that regardless of how many imports the file has or how long the rest of the project's background sync takes.

### 2. `explain_call_site` gains the same JIT fallback `search_dependency_docs` already has
- Today: `symbols.ResolveEvidence(...)` fails or returns nothing if the resolved dependency isn't synced — a dead end.
- Fix: on the same `ErrNoActiveGeneration`-shaped failure `search_dependency_docs` already detects (WATCH-019), trigger a scoped sync for just that one dependency (or bump it, if a sync is already running for the project) and retry once — identical pattern, applied to the tool that's arguably hit *more* often than an explicit name lookup, since tracing a call site is a more natural agent action than already knowing a dependency's exact name.

## Inputs / Outputs
- Input: a file path (`prioritize_file`) or a call site (`explain_call_site`, now JIT-capable).
- Output: the prioritized/synced dependency set, or a bounded still-running response; `explain_call_site` returns real evidence once its dependency is ready, instead of the current dead end.

## Failure behavior
- A file with no resolvable imports (non-Go, or imports outside the project's own dependency graph — e.g. standard library, or another file in the same project) reports zero matched dependencies, not an error — an agent calling this on an unremarkable file should get a quiet, cheap no-op.

## Tests
- A real Go file with N real imports (reuse this session's own established fixture conventions — a small hand-built package with distinguishable imports) correctly prioritizes exactly those N dependencies, verified against SCOPE-003's set-based mechanism directly (not just "it didn't error").
- `explain_call_site` against an unsynced dependency now returns real evidence after a JIT trigger, instead of the empty/dead-end response it returns today — a direct regression test for the exact gap this ticket closes.
- Both tools' bounded-response behavior under a slow/large dependency set matches `sync_project`'s own proven contract (reuse its existing test shape).

## Acceptance criteria
- [x] `prioritize_file` correctly parses a real Go file's imports and prioritizes/syncs exactly the matched dependency set.
- [x] `explain_call_site` no longer dead-ends on an unsynced dependency — it triggers a JIT sync and returns real evidence, bounded the same way `sync_project` already is.
- [x] A non-Go or import-free file is a cheap, correct no-op, not an error.

## Post-implementation notes
- `prioritize_file` (`internal/mcp/prioritize.go`) reads only the file's import block with `go/parser` (`ImportsOnly`, so a body that doesn't compile is irrelevant), matches each import to the project's resolved Go dependency by **longest module-path prefix** (`cloud.google.com/go/billing/apiv1` belongs to `.../go/billing`, not the shorter root module; `.../goblin` is not a sub-path of `.../go`), and requests exactly the not-yet-synced ones as one set.
- Shared helper `ensureDependencies`: if a sync is already running for the project every name is bumped to the front of its queue; otherwise one scoped multi-dependency sync starts (detached, so it keeps running if the wait ends). Waits at most `mcpSyncWaitBound` polling readiness; `still_building: true` is a normal outcome, not a failure. A failed sync surfaces as an error.
- `explain_call_site` now catches `symbolgraph.NotSyncedError` (a new typed error that carries the dependency name and unwraps to `query.ErrNoActiveGeneration`, so existing `errors.Is` checks still match), builds that one dependency next, and retries once. Still-building returns `still_building: true`; a failed sync falls back to the original error, matching `search_dependency_docs`' precedent.
- `SyncTrigger.SyncProject` now takes `[]string` instead of one name (needed for the set); the seven call sites and fakes were updated.
- Gated by `enable_sync_tool` (it triggers builds), like `sync_project`; with it off, a file that needs builds says so instead of silently doing nothing.
- Not changed: `search_dependency_docs`' own JIT path still waits on its sync without the `mcpSyncWaitBound` cap (it was left as is; a long dependency there can block up to its own wait). Worth a follow-up so all three JIT paths share `ensureDependencies`.
- Tested: matching (longest prefix, Go only, stdlib and own packages excluded), import parsing tolerant of a broken body, the one-call exact-set build, the bump-instead-of-second-sync path, the bounded still-building path, already-synced no-op, quiet no-ops and real errors, all four `explain_call_site` outcomes, and the tool over a real in-memory MCP client, all under `-race`.
- **2026-09: verified live.** `TestPrioritizeFileAndExplainCallSiteOverARealDaemon` (`internal/cli/scope_004_live_test.go`, gated behind `RAGCTL_LIVE_BENCHMARK=1` — needs real network/Ollama/Qdrant, matching VALID-003's benchmark precedent) drives a real `ragctl serve` subprocess with a real MCP client over stdio: `explain_call_site` at a call site whose dependency (`github.com/spf13/pflag`) had never been synced correctly triggered a fresh JIT sync and returned real evidence (not the old dead end); `prioritize_file` then correctly matched and synced both of the file's real imports (`pflag`, already-synced from the step before, and `github.com/google/go-cmp`, genuinely new) as one set, and `search_dependency_docs` confirmed go-cmp's content actually landed and is searchable. All three real gaps this ticket exists to close — no JIT wiring on `explain_call_site`, no set-based multi-dependency prioritization, no proof any of it survives a real daemon — are now closed, not just unit-tested.
