# MCP-004: Offline query test

**Epic:** MCP Server
**Status:** planned
**Depends on:** MCP-003
**Estimated size:** small

## Goal
Prove, with an automated test, that `ragctl serve` answers version-correct MCP queries with zero network access once dependency knowledge has been synced — this is the core product thesis and is release-blocking.

## Non-goals
- Performance/load testing of the MCP server.
- Testing every tool — this ticket covers `search_dependency_docs` and `get_dependency_version` as the representative offline path.

## Simplicity constraints
- One integration test, no new production code. If the test reveals a real network dependency at query time, fix that dependency in its owning package rather than adding a workaround here.

## Design
Test location: `internal/mcp/offline_test.go` (or `test/e2e/offline_test.go` if an e2e test dir exists).

Sequence:
```text
1. Pre-sync a fixture dependency (e.g. via a test double or a pre-populated Badger/bbolt fixture, not a live sync) into local stores.
2. Configure the test's HTTP transport / DNS to fail all outbound requests (e.g. httptest with no external dial allowed, or a build-tag-gated network-blocking RoundTripper).
3. Start `ragctl serve` (in-process, not a subprocess, for test speed) with MCP enabled.
4. Send a search_dependency_docs MCP call over stdio/in-process transport.
5. Assert the result is non-empty and its version/generation metadata matches the pre-synced fixture.
```

## Inputs / Outputs
- Input: pre-populated local fixture stores.
- Output: pass/fail test result; this test must run in CI (`go test ./...`).

## Failure behavior
- If any component attempts an outbound network call, the blocked RoundTripper causes an immediate test failure with a clear message identifying the offending call site.

## Tests
This ticket IS the test. No further sub-tests required beyond the sequence above.

## Acceptance criteria
- [ ] Test runs in CI with network access disabled for the test process.
- [ ] Test asserts returned metadata's version/generation matches the fixture exactly.
- [ ] Test is part of the standard `go test ./...` suite (release-blocking, not opt-in/tagged out).
