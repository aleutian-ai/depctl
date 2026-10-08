# VERIFY-001: CI-runnable live MCP-over-stdio smoke test

**Epic:** Continuous Verification
**Status:** done
**Depends on:** none (reuses existing test infra: `useRealDepctlBinary`, `ensureDaemon`, `scanDepFixture`)
**Estimated size:** medium

## Goal
Add a test that spawns a real `depctl daemon`, then a real `depctl serve` subprocess over stdio, and drives it with a real MCP client (`github.com/modelcontextprotocol/go-sdk/mcp`'s client package — already a real dependency of this module via the server side, no new import needed) — the exact shape of session that caught WATCH-019/020's sentinel-identity bug and GRAPH-003's module-path bug, neither of which any existing unit test (all either fully in-process or using `sdkmcp.NewInMemoryTransports()`) could have caught. Run it in CI on every push, so this stops being something a human has to remember to do manually.

## Non-goals
- **No real embedder or vector backend in CI** — standing up Ollama + Qdrant in GitHub Actions adds real cost, install complexity, and flakiness for a benefit this ticket doesn't need: both real bugs this session found were about the wire boundary (error-sentinel propagation, dependency-resolution matching) and neither required actual search *content* to expose or prove. Scope this test to the calls that don't need a live backend — `knowledge_status`, `list_project_dependencies`, `get_dependency_version`, `scan_project`, `explain_call_site` against an internal call site and against a genuinely unresolved dependency, and `search_dependency_docs`'s `ErrNoActiveGeneration` path — mirroring how `internal/cli/query_client_test.go`'s own existing `SearchKnowledge` assertion already avoids needing a live backend (`SearchKnowledge` checks `GetActiveGeneration` before ever touching an embedder). A full real-embedder+real-vector-backend smoke test remains a manual/local exercise (see `docs/offline-quickstart.md`), not a CI requirement — reconsider this non-goal only if a future bug specifically requires live-backend content to catch.
- No new MCP client dependency — the SDK's client package ships in the same module as the server package this codebase already depends on.
- Not a replacement for the existing in-process `internal/mcp` tests (`sdkmcp.NewInMemoryTransports()`-based) — those stay, testing handler logic fast; this adds the one thing they can't cover.

## Simplicity constraints
- Lives in `internal/cli` alongside the other real-daemon integration tests (`query_client_test.go`, `symbolgraph_wiring_test.go`) — same `isolateEnv`/`requireGo`/`runInitForTest`/`useRealDepctlBinary`/`scanDepFixture` helpers, no new test-infra package.
- One test function spawning one `depctl serve` subprocess and making several tool calls against it in sequence, not one subprocess per assertion — subprocess spawn is the expensive part.

## Design
`internal/cli/serve_smoke_test.go` (new):
```go
func TestServeOverRealStdioTransport(t *testing.T) {
    isolateEnv(t)
    requireGo(t)
    runInitForTest(t)
    useRealDepctlBinary(t)
    root := scanDepFixture(t) // example.com/app depending on example.com/foo, local replace

    cmd := exec.Command(depctlBinaryPath(t), "serve")
    cmd.Dir = root
    cmd.Env = testEnv(t)

    client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "verify-001", Version: "0.0.1"}, nil)
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    session, err := client.Connect(ctx, &sdkmcp.CommandTransport{Command: cmd}, nil)
    // ... assertions against session.CallTool for each tool listed above
}
```
Each assertion mirrors an existing in-process or real-daemon test's expected behavior (e.g. `knowledge_status` should report the one project `scanDepFixture` registered; `search_dependency_docs` against the unsynced `example.com/foo` should return an `ErrNoActiveGeneration`-derived message, not a raw error) — this ticket doesn't invent new expected behavior, it proves the already-specified behavior survives the real subprocess/stdio/daemon-HTTP round trip, which is exactly where both real bugs this session found were hiding.

## Inputs / Outputs
- Input: none beyond the existing fixture/harness.
- Output: one new CI-covered test proving the real MCP-over-stdio path end to end for every tool call that doesn't require a live backend.

## Failure behavior
- Subprocess fails to start / connect within the timeout → test failure naming the stderr output, not a hang (existing `ensureDaemon`-style patterns in this codebase already establish this convention).

## Tests
This ticket's own test *is* the test — see Design.

## Acceptance criteria
- [x] A real `depctl serve` subprocess, connected to over stdio by a real MCP client, correctly answers `knowledge_status`, `list_project_dependencies`, `get_dependency_version`, `scan_project`, `explain_call_site` (internal call site + unresolved dependency), and `search_dependency_docs`'s no-active-generation path.
- [x] This test runs as part of `go test ./...` (no separate CI job/service container needed, per the non-goals above) — i.e. it's simply part of `.github/workflows/test.yml`'s existing `go test ./...` step, nothing new to configure.
- [x] The test fails clearly (not by hanging) if the subprocess never connects or a tool call times out.

## Post-implementation note
Implemented as `TestServeOverRealStdioTransport` in `internal/cli/serve_smoke_test.go`, passing on the first real run (2.7s). One deviation from the design sketch worth noting: `scanDepFixture` only writes a `go.mod` (no `.go` source), so `explain_call_site` needs real source to resolve against — the test writes its own small `main.go` (an internal `helper()` call plus a `fmt.Println` for the stdlib-unresolved-dependency case) into the fixture root after scanning. No new CI configuration needed; this is just another `go test ./...` test.
