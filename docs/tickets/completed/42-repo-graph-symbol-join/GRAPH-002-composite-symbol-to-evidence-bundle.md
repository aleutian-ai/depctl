# GRAPH-002: Composite symbol-to-evidence bundle

**Epic:** Repo-Graph Symbol Join
**Status:** done
**Depends on:** GRAPH-001 (`SymbolProvider`/`ExternalSymbolRef`), `internal/control/bbolt.Store` (`GetResolution`), `internal/query.Service` (`SearchKnowledge`)
**Estimated size:** medium

## Goal
Implement the actual join described in the scratch doc's §14: a call site resolves (via GRAPH-001's `SymbolProvider`) to an `ExternalSymbolRef`, which this ticket maps to a `domain.Dependency`/version depctl already knows about (via the project's stored `domain.Resolution`), and then queries `internal/query.Service` for that exact dependency+version's evidence — producing one compact evidence bundle for a call site, end to end.

## Non-goals
- **Choosing exactly one graph-provider family to prototype against is explicitly out of scope for this ticket.** This ticket depends only on GRAPH-001's `SymbolProvider` interface; whoever picks up GRAPH-002 supplies (or stubs, for tests) a concrete `SymbolProvider` — deciding which real provider (SCIP/LSP/CodebaseMemory/GitNexus/etc.) to integrate against is deferred to that implementer's judgment, not decided here.
- No new persistent storage — this ticket reads `internal/control/bbolt.Store`'s existing resolution state and calls `internal/query.Service` as-is; no new bbolt bucket, no new cache.
- No MCP tool / CLI command wiring in this ticket — this is the join logic itself (a `internal/symbolgraph` function/type), callable from a future MCP tool or CLI command, but that transport-layer wiring is separate scope.
- No fuzzy/best-effort dependency matching — if `ExternalSymbolRef.Module` doesn't match any dependency in the project's stored `Resolution.Dependencies` exactly, the join fails cleanly (see Failure behavior), it does not guess at a near-match.

## Simplicity constraints
- The join is a single function over existing types — no new domain model beyond what's needed to carry a call site's resulting evidence bundle back to a caller.
- Reuse `query.ControlStore`'s existing `GetResolution(ctx, projectID) (domain.Resolution, error)` — do not add a new bbolt method; `Resolution.Dependencies []domain.DependencyVersion` already has everything needed to match `ExternalSymbolRef.Module` against a known dependency's `Dependency.Name`.
- Reuse `query.Service.SearchKnowledge` unchanged for the actual evidence retrieval — this ticket's only job is turning a resolved symbol into the right `query.Query{ProjectID, Dependency, Mode: query.ModeProject, Text: ...}`, not reimplementing search.

## Design
Package: `internal/symbolgraph` (extends GRAPH-001's package)

```go
// EvidenceBundle is the end-to-end result of resolving one call site to
// the dependency-version evidence relevant to it.
type EvidenceBundle struct {
    Symbol     ExternalSymbolRef
    Dependency domain.DependencyVersion
    Result     query.SearchResult
}

// Resolver joins a project source call site to the exact-version
// knowledge evidence depctl already has for the dependency it calls
// into — the "graph resolves symbol, depctl resolves knowledge" join
// from the design doc's repo-graph section.
type Resolver struct {
    symbols  SymbolProvider  // GRAPH-001
    control  ControlStore    // narrow slice, see below
    queries  QueryService    // narrow slice, see below
}

// ControlStore is the narrow slice of *bbolt.Store this package needs
// — just enough to map an ExternalSymbolRef to a project's already-
// resolved dependency version.
type ControlStore interface {
    GetResolution(ctx context.Context, projectID string) (domain.Resolution, error)
}

// QueryService is the narrow slice of *query.Service this package
// needs to turn a resolved dependency into an evidence bundle.
type QueryService interface {
    SearchKnowledge(ctx context.Context, q query.Query) (query.SearchResult, error)
}

// New returns a Resolver wired against the given provider/stores.
func New(symbols SymbolProvider, control ControlStore, queries QueryService) *Resolver

// ResolveEvidence resolves site to its external symbol (via symbols),
// matches it against projectID's stored resolution, and searches that
// exact dependency+version for evidence relevant to queryText. Returns
// (nil, nil) — not an error — if site does not resolve to an external
// symbol at all (an internal-to-project call site is not a failure,
// just nothing to join).
func (r *Resolver) ResolveEvidence(ctx context.Context, projectID string, site CallSite, queryText string) (*EvidenceBundle, error)
```

`ResolveEvidence`'s join logic:
1. `ref, ok, err := r.symbols.Resolve(ctx, site)` — propagate `err`; return `(nil, nil)` if `!ok`.
2. `resolution, err := r.control.GetResolution(ctx, projectID)` — propagate `err`.
3. Find the `domain.DependencyVersion` in `resolution.Dependencies` whose `Dependency.Name == ref.Module` (and, where the ecosystem is known, `Dependency.Ecosystem == domain.Ecosystem(ref.Ecosystem)`) — exact match only, per Simplicity constraints.
4. No match → return a typed `ErrDependencyNotResolved` (distinct from "not an external symbol" in step 1) — the project genuinely doesn't have this module in its resolution, which is worth surfacing distinctly.
5. Match found → call `r.queries.SearchKnowledge(ctx, query.Query{ProjectID: projectID, Dependency: matched.Dependency.Name, Text: queryText, Mode: query.ModeProject})`, wrap the result with `ref`/`matched` into `EvidenceBundle`.

## Inputs / Outputs
- Input: `projectID` (an already-registered depctl project), a `CallSite`, and `queryText` (what to search for once the exact dependency+version is known — e.g. the symbol's own qualified name, or a caller-supplied natural-language question about it).
- Output: `*EvidenceBundle` (symbol + matched dependency version + search result), or `(nil, nil)` when the call site isn't external, or a typed error for provider/resolution/search failures.

## Failure behavior
- `SymbolProvider.Resolve` returns an error → propagated as-is, wrapped with call-site context (`fmt.Errorf("symbolgraph: resolve %s:%d:%d: %w", ...)`).
- `ControlStore.GetResolution` returns `bbolt.ErrNotFound` (project not registered / never resolved) → propagated, wrapped distinctly from "dependency not in this resolution" so a caller can tell "no project state at all" from "project exists but doesn't depend on this."
- `ExternalSymbolRef.Module` doesn't match anything in `resolution.Dependencies` → `ErrDependencyNotResolved`, not silently empty evidence — the caller (an agent) should be told the symbol is genuinely outside what depctl knows about this project, not shown a misleadingly empty result.
- `QueryService.SearchKnowledge` returns `query.ErrDependencyNotFound`/`ErrNoActiveGeneration` → propagated as-is; this ticket adds no new handling for those, they're already meaningful to a caller.

## Tests
- A fake `SymbolProvider` resolving a call site to a known `ExternalSymbolRef`, matched against a fixture `domain.Resolution` containing that module, and a fake `QueryService` — end-to-end `ResolveEvidence` returns the expected `EvidenceBundle`.
- `SymbolProvider` returns `ok == false` (internal call site) → `ResolveEvidence` returns `(nil, nil)`, not an error.
- `ExternalSymbolRef.Module` not present in the project's `Resolution.Dependencies` → `ErrDependencyNotResolved`.
- `SymbolProvider.Resolve` returns an error → propagated, not swallowed.
- `ControlStore.GetResolution` returns "project not found" → propagated distinctly from the "dependency not resolved" case.

## Acceptance criteria
- [x] `internal/symbolgraph.Resolver.ResolveEvidence` implements the full join: call site → `ExternalSymbolRef` → matched `domain.DependencyVersion` → `query.SearchResult`.
- [x] `ErrDependencyNotResolved` is distinct from "not an external symbol" and from "project not found," each independently testable.
- [x] No specific graph-provider implementation is chosen, evaluated, or hardcoded — `Resolver` depends only on `SymbolProvider`.
- [x] `internal/query.Service.SearchKnowledge` is called unchanged; no duplicate search/ranking logic added in `internal/symbolgraph`.
