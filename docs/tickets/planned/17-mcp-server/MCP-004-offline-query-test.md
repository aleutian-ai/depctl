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
- [x] Test runs in CI with network access disabled for the test process.
- [x] Test asserts returned metadata's version/generation matches the fixture exactly.
- [x] Test is part of the standard `go test ./...` suite (release-blocking, not opt-in/tagged out).

## Post-implementation note
The design sketch's "configure the test's HTTP transport / DNS to fail all outbound requests" was implemented as a custom `http.Transport.DialContext` that rejects (and records) any dial to a non-loopback address — real *production* HTTP client code (`internal/embedding/ollama`, `internal/backend/qdrant`) runs against real local services behind it: a real Qdrant container (same pattern as epic 13's integration tests) and a real `httptest.Server` standing in for Ollama, both bound to 127.0.0.1. This is deliberately stronger than wiring in fake `embedding.Embedder`/`backend.VectorBackend` implementations, which would trivially never make a network call and prove nothing about the actual HTTP client code paths. `TestLoopbackOnlyClientBlocksNonLoopbackDials` separately proves the blocking mechanism itself actually fires (a real request to `example.com` gets rejected before any DNS/TCP activity) — without it, a bug making the blocker permissive (an inverted condition, say) would let the main test pass for the wrong reason: not because nothing left the loopback interface, but because nothing was actually blocking it.

Per the ticket's own guidance ("via a test double or a pre-populated Badger/bbolt fixture, not a live sync"), the fixture is written directly into bbolt/Badger (`PutProject`, `PutResolution`, `PutGeneration`+`PromoteGeneration`, `PutKnowledgeObject`, `PutChunk`) rather than by running `generation.Build`/`Replicate`/`validate.Run`/`promote.Promote` — those are already proven for real elsewhere (epics 11, 14). The one exception: the chunk's vector point is embedded and upserted for real (through the network-blocked, loopback-only clients), since MCP-004's actual subject is the query path reading real vector-backend/Badger data, not the acquisition pipeline.

Test skips cleanly (`requireContainerRuntime`) when no Docker-API-compatible runtime is reachable — same convention as `internal/backend/qdrant`'s own integration test — including inside `hack/test-linux.sh`'s own Alpine/Podman container, which has no nested container runtime.
