# STRESS-015: Concurrent real MCP sessions

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
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
- [x] N (≥10) concurrent real MCP sessions issuing read-only tool calls all complete correctly, with no hangs and no cross-talk between sessions.
- [x] Aggregate latency/throughput under this load is recorded.

## Post-implementation note (2026-09-29)

Run directly against real production rather than an isolated fixture — this ticket's own scope is read-only (no `sync_project`/`scan_project`), so it needed no separate setup and could test "the same real project fleet" literally, matching the ticket's own framing. First restarted the real daemon (it was running a build from before today's session's fixes) to test against current code.

10 separate real `ragctl serve` subprocesses (10 genuinely independent MCP client sessions, the harder shape per the ticket's own step 2), each assigned a different one of ragctl's own real, already-synced dependencies (`badger`, `toml`, `go-spew`, `xxhash`, `backoff`, `mergo`, `go-winio`, `containerd/log`, `pty`, `go-md2man`), each issuing a sustained burst of 5 rounds × 3 read-only tool calls (`search_dependency_docs`, `list_project_dependencies`, `get_dependency_version`) — 150 total real calls.

**Result: all 150 calls completed correctly. Zero hangs, zero dropped calls, zero cross-talk** (every response's own `dependency`/`package` field was checked to confirm it matched what that specific session asked for, never another session's). Total wall-clock 15.6s, aggregate throughput ~9.6 calls/sec across the whole concurrent burst. `ragctl doctor` confirmed the daemon itself remained healthy throughout (no corruption from the read-path concurrency).

**Incidental, unrelated real finding (not caused by this test, not fixed here):** `doctor` flagged 101 real dependencies across the user's other real local projects (not the one this test used) as "referenced but never built" (OPS-005's own check) — confirmed pre-existing, not caused by STRESS-015's read-only calls, since that check only flags references older than its 5-minute grace window. Also spotted `/tmp/ragctl-opencode-e2e/testproj` and `/private/tmp/ragctl-opencode-e2e/testproj` registered as two separate real projects — live, real-world corroboration of the exact symlink-path bug `PROJ-004` (fixed earlier the same day) describes, predating that fix. Neither is in this ticket's scope to fix (a large, unplanned action against real production data spanning many unrelated projects) — flagged to the user for awareness/decision rather than acted on autonomously.
