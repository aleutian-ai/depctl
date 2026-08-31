# VEC-010: Backend conformance suite

**Epic:** Remaining Vector Backends
**Status:** planned
**Depends on:** VEC-002 (Qdrant adapter)
**Estimated size:** medium

## Goal
Build a shared test suite, runnable against any `VectorBackend` implementation, that every backend adapter must pass: health, namespace setup, upsert, metadata filter, query, generation filter, delete, idempotent upsert.

## Non-goals
- Implementing any additional backend (VEC-011..014 do that, reusing this suite).
- Performance/load testing — functional correctness only.

## Simplicity constraints
- One shared Go test helper function taking a `VectorBackend` instance, not a full separate test framework.
- Reuse `testcontainers-go` (already a project dependency for VEC-002's Qdrant integration test) rather than adding another test-infra dependency.

## Design
Package: `internal/backend/conformance` (or `internal/backend/backendtest`)

```go
func RunConformanceSuite(t *testing.T, newBackend func(t *testing.T) backend.VectorBackend)
```
Subtests (`t.Run`) covering the `VectorBackend` interface from VEC-001:
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
```
Each backend adapter's own test file calls `conformance.RunConformanceSuite(t, newBackendFn)`.

## Inputs / Outputs
- Input: a constructor function producing a fresh backend instance per test (typically wired to a testcontainer).
- Output: pass/fail per subtest, reusable across all adapters.

## Failure behavior
- A backend that lacks a capability (e.g. no hybrid search) should be handled via the `Capabilities` struct — the suite skips capability-gated subtests rather than failing outright.

## Tests
This ticket *is* the test suite. Validate it first against the existing Qdrant adapter (VEC-002) to confirm it catches real regressions (e.g. temporarily break Qdrant's delete-by-filter and confirm the suite fails).

## Acceptance criteria
- [ ] Suite passes against the Qdrant adapter unmodified.
- [ ] Suite is parameterized purely via the `VectorBackend` interface — no Qdrant-specific assumptions leak in.
- [ ] Includes: health, namespace setup, upsert, metadata filter, query, generation filter, delete, idempotent upsert (re-upsert same ID does not duplicate).
