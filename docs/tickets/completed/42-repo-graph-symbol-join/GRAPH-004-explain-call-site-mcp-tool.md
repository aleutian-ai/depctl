# GRAPH-004: `explain_call_site` MCP tool

**Epic:** Repo-Graph Symbol Join
**Status:** done
**Depends on:** GRAPH-002 (`symbolgraph.Resolver`/`EvidenceBundle`), GRAPH-003 (a real `SymbolProvider`), `internal/mcp` (existing tool-registration pattern)
**Estimated size:** small

## Goal
Wire GRAPH-002's join into a seventh MCP tool, `explain_call_site`, so an agent can ask "what does this call site mean" directly by file/line/column instead of having to already know the dependency name and phrase a `search_dependency_docs` query itself — the actual end-user-visible point of this whole epic. Without this ticket, GRAPH-001-003 produce a real, tested Go library nothing calls.

## Non-goals
- No change to `search_dependency_docs` itself — this is a new, separate tool, not an alternate input mode bolted onto an existing one (keeps each tool's input shape simple and its `In`/`Out` types self-describing, matching every other tool in `internal/mcp/tools.go`).
- No editor/IDE-side integration (no "jump to call site" UX) — the tool takes an explicit file/line/column the calling agent already has from its own view of the source, same as any other MCP tool takes explicit arguments.
- No JIT-sync-on-miss for this tool (unlike WATCH-019's `search_dependency_docs` behavior) — if the resolved dependency has no active generation, `explain_call_site` reports that plainly (same `toolError` treatment) rather than duplicating WATCH-019's sync-and-retry logic; that can be added later as its own small follow-up if real usage shows it's worth it, matching how WATCH-019 itself was scoped narrowly on first landing.

## Simplicity constraints
- Thin adapter only, matching every other tool in `internal/mcp/tools.go`: parse input, call `symbolgraph.Resolver.ResolveEvidence`, format the result. No business logic here beyond that.
- `internal/mcp` defines its own narrow consumer-side interface for this (matching how `QueryService`/`SyncTrigger`/`PriorityBumper` are already narrow, mcp-owned interfaces, not direct imports of concrete implementation types) rather than importing `internal/symbolgraph.Resolver` concretely.

## Design
`internal/mcp/server.go`:
```go
// CallSiteResolver lets explain_call_site join a project source call
// site to the dependency-version evidence relevant to it (GRAPH-002) —
// narrow, consumer-side, satisfied by *symbolgraph.Resolver.
type CallSiteResolver interface {
    ResolveEvidence(ctx context.Context, projectID string, site symbolgraph.CallSite, queryText string) (*symbolgraph.EvidenceBundle, error)
}
```
`Deps` gains `Symbols CallSiteResolver` (may be nil — mirrors `Sync`/`Priority`'s existing nil-safety pattern; a nil `Symbols` means `explain_call_site` is registered but always reports "not configured," never silently absent, matching `sync_project`'s existing disabled-by-config precedent).

`internal/mcp/tools.go`:
```go
type ExplainCallSiteIn struct {
    ProjectID string `json:"project_id" jsonschema:"the registered project ID"`
    File      string `json:"file" jsonschema:"path to the source file, relative to the project root"`
    Line      int    `json:"line" jsonschema:"1-indexed line number of the call site"`
    Column    int    `json:"column" jsonschema:"1-indexed column number of the call site"`
    Query     string `json:"query,omitempty" jsonschema:"optional — what to ask about the resolved symbol; defaults to the symbol's own qualified name if omitted"`
}

type ExplainCallSiteOut struct {
    Symbol     *ResolvedSymbol      `json:"symbol,omitempty"`
    Chunks     []SearchResultChunk  `json:"chunks,omitempty"`
    Note       string               `json:"note"`
}

type ResolvedSymbol struct {
    Ecosystem     string `json:"ecosystem"`
    Module        string `json:"module"`
    Package       string `json:"package"`
    QualifiedName string `json:"qualified_name"`
    Version       string `json:"version"`
}
```

Handler behavior:
- `symbols == nil` → `toolError`-style message: "call-site resolution is not configured for this server."
- `ResolveEvidence` returns `(nil, nil)` (call site isn't external) → `ExplainCallSiteOut{Note: "this call site refers to code inside the project, not an external dependency"}`, not an error — a legitimate, informative non-match.
- `ResolveEvidence` returns `symbolgraph.ErrDependencyNotResolved` → `toolError`-mapped message pointing at `scan_project`/`sync_project`, same spirit as `ErrProjectNotFound`/`ErrDependencyNotFound` today.
- Success → `ExplainCallSiteOut` populated from the `EvidenceBundle`, reusing the exact same `SearchResultChunk` shaping `search_dependency_docs` already does (no duplicate mapping code — extract the existing chunk-mapping loop into a small shared helper both handlers call).

## Inputs / Outputs
- Input: `ExplainCallSiteIn` (project ID, file/line/column, optional query text).
- Output: `ExplainCallSiteOut` — resolved symbol + version-correct evidence chunks, or a clear non-match/error note.

## Failure behavior
- Matches GRAPH-002's own failure taxonomy (provider error / project-not-found / dependency-not-resolved / query-service error), each mapped through `toolError` the same way existing tools already are — no new error-handling pattern introduced.

## Tests
- End-to-end: a fixture project with a real external call site, `explain_call_site` returns the correct symbol and non-empty evidence chunks.
- A call site resolving internally → `Note` set, `Symbol`/`Chunks` empty, no error.
- `symbols == nil` (server started without call-site resolution configured) → clear "not configured" message, not a panic or an unqualified generic error.
- `ErrDependencyNotResolved` → actionable `toolError`-mapped message.

## Acceptance criteria
- [x] `explain_call_site` registered as a seventh MCP tool, following the exact thin-adapter shape every other tool already uses.
- [x] A real end-to-end call (fixture project, real `gopackages.Provider` from GRAPH-003) returns correct, version-scoped evidence for an external call site.
- [x] Internal-call-site and not-configured cases are informative, not errors dressed up as failures.
- [x] `docs/internal/mcp.md` and `docs/features/query-serving.md` updated to include the new tool, matching this repo's existing convention of keeping those docs in sync with shipped tool surface.

### Code review + live testing, before this ticket was trusted for real use
Per the same discipline WATCH-019/020 established this session (test-suite-green is necessary but not sufficient), this ticket got both a code review pass and a real live-test pass before being treated as done.

**Code review found one real bug (see GRAPH-003's own post-implementation note for the fix)**: `ExternalSymbolRef.Module` was a package path, not a module path, breaking the join for any multi-package dependency. Fixed before live testing, not after — live testing then served as *confirmation* of the fix against a real published dependency, not as the discovery mechanism this time.

**Code review also found `daemonResolutionStore` (this ticket's daemon-HTTP adapter for `symbolgraph.ControlStore`) had zero test coverage against a real daemon** — exactly the shape of gap that made WATCH-019/020 dead code in production before that fix. Added `TestDaemonResolutionStoreRoundTripsThroughRealDaemon` and `TestDaemonResolutionStoreUnknownProjectPropagatesError` (`internal/cli/symbolgraph_wiring_test.go`) — both pass against a real, separately-spawned daemon.

**Live test session**: built the real `depctl` binary, created a fixture project (`example.com/liveapp`) with a genuine `go.mod` dependency on `github.com/stretchr/testify v1.9.0` (resolved via a real `go mod tidy`, not a local replace), `depctl init`/`scan`/`sync --dependency github.com/stretchr/testify` against the existing `depctl-qdrant` container and a local Ollama embedder, then called `explain_call_site` through a real spawned `depctl serve` process via a standalone Go MCP client (the same pattern used for WATCH-019's live test). Three calls:
- A call site at `assert.Equal(...)`: returned `module: "github.com/stretchr/testify"` (module root, not the `assert` subpackage path — the exact case the code-review fix targets), `package: "assert"`, `qualified_name: "Equal"`, `version: "v1.9.0"`, and 10 real, relevant, version-correct chunks sourced from the real synced generation. ~1.8s round trip (already-synced dependency).
- A call site inside the same project (`helper()`): correctly returned `{"note": "this call site refers to code inside the project, not an external dependency"}` — no error, no spurious symbol.
- A call site on a stdlib call (`fmt.Println`): correctly resolved as an *external* symbol (stdlib isn't the home module) but then correctly reported `ErrDependencyNotResolved` via `toolError`, since depctl's own dependency resolver doesn't track the standard library as a project dependency — expected behavior given this tool's scope, not a bug, but worth knowing: `explain_call_site` will never resolve evidence for a stdlib call site, only for tracked project dependencies.

Full repo `go build`/`go vet`/`gofmt -l .`/`go test ./...` clean after both the review fix and the new tests.
