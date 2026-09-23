# STRESS-015: Concurrent real MCP sessions

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none
**Estimated size:** medium

## Goal
Run multiple real `ragctl serve` sessions (or one session handling multiple concurrent tool calls, whichever the MCP SDK's transport actually supports — check before assuming) against one daemon, issuing a realistic mix of read-only tool calls (`search_dependency_docs`, `list_project_dependencies`, `get_dependency_version`, `explain_call_site`) concurrently, simulating several real agent sessions working against the same project fleet at once. VERIFY-001 proves one session's sequential tool calls work; this proves several sessions' concurrent calls do too.

## Non-goals
- No write-tool (`sync_project`/`scan_project`) concurrency stress here — that's covered by STRESS-007/008's sync-specific scenarios; this ticket is about read-path concurrency specifically.

## Simplicity constraints
- Reuse VERIFY-001's real-MCP-client pattern (`internal/cli/serve_smoke_test.go`'s approach, or a standalone driver like the ones used earlier in live testing this session), scaled up to N concurrent clients/calls instead of one sequential session.

## Design
1. A project with already-synced content (reuse STRESS-005's).
2. Spawn N (e.g. 10) real MCP client connections against one or more `ragctl serve` subprocesses.
3. Issue a realistic mix of read-only tool calls concurrently across all N connections — different queries, different dependencies, repeated for a sustained burst (not just one round).
4. Confirm every call completes with a correct result, no hangs, no cross-talk (one session's query never returns another session's or another project's content), and record aggregate latency/throughput.

## Inputs / Outputs
- Input: N concurrent real MCP client sessions against a synced project.
- Output: pass/fail on correctness and no-hang/no-cross-talk, plus a latency/throughput data point under this load shape.

## Failure behavior
- Any hang, dropped call, or cross-talk (wrong content returned to the wrong caller) is this ticket's finding — cross-talk in particular would be a serious, release-blocking bug.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] N (≥10) concurrent real MCP sessions issuing read-only tool calls all complete correctly, with no hangs and no cross-talk between sessions.
- [ ] Aggregate latency/throughput under this load is recorded.
