# VEC-001: Vector backend interface

**Epic:** Vector Backend (Qdrant)
**Status:** planned
**Depends on:** EMB-001
**Estimated size:** small

## Goal
Define the narrow `VectorBackend` interface and namespace/metadata model that every vector database adapter (starting with Qdrant) implements.

## Non-goals
- No concrete adapter (VEC-002).
- No hybrid/lexical search abstraction — `Capabilities` just reports what's available; a lowest-common-denominator query model is explicitly avoided per the design spec.

## Simplicity constraints
- Six methods only, matching the design spec exactly — do not add speculative methods (e.g. batch update, aggregate queries) until a real use case needs them.
- Metadata filter fields are fixed to the required v0.1 set (package, ecosystem, version, generation, plus source type/authority per design spec) — do not build a generic arbitrary-filter query DSL.

## Design
- Package: `internal/backend`.
```go
type VectorBackend interface {
    Name() string
    Capabilities(ctx context.Context) (Capabilities, error)
    EnsureNamespace(ctx context.Context, ns Namespace) error
    Upsert(ctx context.Context, req UpsertRequest) error
    Delete(ctx context.Context, req DeleteRequest) error
    Query(ctx context.Context, req QueryRequest) (QueryResult, error)
    Health(ctx context.Context) error
}

type Capabilities struct {
    VectorSearch   bool
    KeywordSearch  bool
    HybridSearch   bool
    MetadataFilter bool
    DeleteByFilter bool
}
```
- Point/metadata model, mandatory fields per point: `ecosystem`, `dependency` (package name), `version`, `generation` (generation ID), `source_type`, `authority`. Represented as a `map[string]any` or small struct attached to each vector in `UpsertRequest`.
- `Namespace` identifies a collection/index scope (e.g. one collection per backend, filtered by metadata — decide per-adapter; Qdrant uses one collection with metadata filters per the design spec's namespace model).

## Inputs / Outputs
- Input: chunk vectors + metadata (from embedding + generation pipeline).
- Output: query results with metadata, or upsert/delete acknowledgements.

## Failure behavior
- All methods return typed errors; no panics. `Health` is a cheap read-only check used by `doctor`/`status`.

## Tests
- Interface compiles against a fake in-memory backend used by VAL-* and MCP-* tests.

## Acceptance criteria
- [x] `internal/backend` package defines `VectorBackend`, `Capabilities`, `Namespace`, `UpsertRequest`, `DeleteRequest`, `QueryRequest`, `QueryResult` with no Qdrant-specific code.
