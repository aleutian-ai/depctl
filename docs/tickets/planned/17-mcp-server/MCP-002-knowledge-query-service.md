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
- [x] Service automatically resolves project → version filters without caller needing to know the version.
- [x] All four query modes implemented and unit-tested against a fake `VectorBackend`.
- [x] No MCP/HTTP/gRPC transport types imported by this package.

## Post-implementation note
The sketched `Service` struct (just `control`/`backend`) was missing two things a working implementation genuinely needs: an `embedding.Embedder` (nothing turns `Query.Text` into the vector `VectorBackend.Query` requires without one) and a `backendName`/`Namespace` (both `GetActiveGeneration` and `VectorBackend.Query` are backend-scoped — same reasoning VAL-004/GEN-002 already established for `Promote`/`Replicate`). Also added `DataStore` (Badger): `backend.ScoredPoint` carries an ID/score/metadata only, never chunk text — a vector point is not the retrievable content itself, so `SearchKnowledge` reads each match's actual `Content` from Badger via `GetChunk` after the vector query returns, and `GetProvenance` reads through a chunk's parent `KnowledgeObject` for `SourceURI` (which lives only on the object, never on a vector point's fixed-schema metadata).

`Query.Dependency` is effectively **required** for every mode except a to-be-supported multi-dependency case, not truly "optional" as the design's comment suggested: `backend.Filter` has one `Dependency`/`Version` field, not a list, so there's no coherent single filter for "search across all of a project's dependencies at once." `ModeAllRetained` is the one mode that searches across every retained version of a *named* dependency (still requires `Dependency`, just not a resolved-by-project version).

`ModeLatest`/`ModeCompare` depend on a `"latest"`-reason `VersionReference` existing for the dependency — nothing in the codebase currently populates that reason (`RET-001`'s reasons are `project`/`manual_pin`/`grace_period` from actual planner/retention flows; `latest` is only ever written by a human or a future upstream-release-tracking feature, neither of which exists yet). Both modes are implemented and tested against a manually-seeded `latest` reference; without one, they correctly return `ErrDependencyNotFound`.

`GetProvenance`'s signature changed from `(ctx, resultID string)` to `(ctx, generationID, chunkID string)` — Badger's chunk key is `chunk/<generation-id>/<chunk-id>` (STORE-003), so a single opaque `resultID` can't address a chunk without generation context; a `SearchResult`'s own `ResultChunk.Generation` field already supplies that, so callers have what they need.

`GetReleaseChanges` was added even though it's MCP-003's tool, not one of MCP-002's four sketched service methods — it needs the same `ControlStore`/`DataStore` access every other method does, and `internal/mcp` isn't supposed to contain business logic, so it belongs here. It reads `Metadata["content_type"]`/`Metadata["release_version"]` directly off Badger chunks (NORM-005's tagging) rather than doing a vector search — there's no semantic query involved, just "give me the exact section for this exact version" — and deliberately does **not** walk every version between `from` and `to`: no version-ordering/semver-range utility exists anywhere in this codebase (naive lexical comparison would misorder `v2.0.0` before `v10.0.0`), and building one is out of scope for wiring existing pieces into MCP tools. It returns excerpts for the two exact versions only.

`Status` (knowledge_status's backing method) was also added beyond the four sketched methods, for the same "business logic belongs here, not in `internal/mcp`" reason — a coarse fleet-wide tally (registered projects × resolved dependencies × active-generation state), explicitly not a full `PLAN-001` diff (no registry access, no fresh resolution).
