# MCP-002: Knowledge query service

**Epic:** MCP Server
**Status:** planned
**Depends on:** VEC-002, STORE-001
**Estimated size:** medium

## Goal
Build the internal, transport-agnostic query service that resolves a project's dependency versions and performs version-filtered knowledge search — the business logic behind every MCP tool.

## Non-goals
- MCP tool wiring / protocol exposure (MCP-003).
- Retrieval quality tuning (hybrid search, reranking) — out of scope for v0.1; a straight vector query with metadata filters is enough.

## Simplicity constraints
- One service struct with plain methods; no generic "query pipeline" or middleware framework.
- Query modes limited to what's specified: `project`, `latest`, `compare`, `all-retained`. Do not add speculative modes.

## Design
Package: `internal/query`.

```go
type Query struct {
    ProjectID  string
    Text       string
    Dependency string // optional: ecosystem+name, narrows search
    Mode       QueryMode // "project" | "latest" | "compare" | "all-retained"
}

type Service struct {
    control ControlStore   // bbolt-backed, from STORE-001
    backend VectorBackend  // from VEC-002
}

func (s *Service) SearchKnowledge(ctx context.Context, q Query) (SearchResult, error)
func (s *Service) GetProjectDependencies(ctx context.Context, projectID string) ([]ProjectDependency, error)
func (s *Service) GetDependencyVersion(ctx context.Context, projectID, pkg string) (DependencyVersion, error)
func (s *Service) GetProvenance(ctx context.Context, resultID string) (Provenance, error)
```

`SearchKnowledge` for `Mode: "project"` resolves `ProjectID` → active dependency version(s) via bbolt, then issues a `VectorBackend.Query` with a metadata filter on `{ecosystem, package, version, generation}` matching the ACTIVE generation. `"latest"` filters to the latest known version instead. `"compare"` issues two queries and merges/labels results. `"all-retained"` (admin/debug) omits the version filter.

## Inputs / Outputs
- Input: `Query` struct.
- Output: `SearchResult` (chunks + metadata: source URI, authority, version, generation), or typed errors (`ErrProjectNotFound`, `ErrDependencyNotFound`, `ErrNoActiveGeneration`).

## Failure behavior
- Project has no resolved dependency matching `Dependency` → `ErrDependencyNotFound`, not an empty result silently returned.
- No active generation for the resolved version → `ErrNoActiveGeneration` (distinct from "no results").

## Tests
- `project` mode resolves the correct version filter for a known project+dependency and forwards it to the backend query.
- `compare` mode issues two queries and labels each result with its version.
- Unknown project ID and unknown dependency each return distinct typed errors.

## Acceptance criteria
- [ ] Service automatically resolves project → version filters without caller needing to know the version.
- [ ] All four query modes implemented and unit-tested against a fake `VectorBackend`.
- [ ] No MCP/HTTP/gRPC transport types imported by this package.
