# VEC-010: Backend conformance suite

**Epic:** Remaining Vector Backends
**Status:** done (2026-10-05)
**Depends on:** VEC-002 (Qdrant adapter)
**Estimated size:** medium

## Goal
Build a shared test suite, runnable against any `VectorBackend` implementation, that every backend adapter must pass: health, namespace setup, upsert, metadata filter, query, generation filter, delete, idempotent upsert.

## Non-goals
- Implementing any additional backend (VEC-011, VEC-014, and VEC-015 do that, reusing this suite).
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

## Post-implementation note (2026-10-05)
`internal/backend/conformance.Run(t, newBackend)` runs 13 subtests against any `VectorBackend`, each with a fresh backend and its own namespace, so one container backs a whole run. Beyond the ticket's list, it covers `Count` (added to the interface after this ticket was written, for `doctor`) and three properties past real bugs made worth guarding:
- delete-by-filter ANDs every field (epic 14 shipped a Qdrant delete that ORed them);
- callers get their own IDs back, even when the backend stores derived ones (Qdrant requires UUIDs);
- namespaces are isolated, the property `VEC-016` depends on.

The delete-by-filter subtests skip via `Capabilities.DeleteByFilter`, as specified.

**Runs against:** the real Qdrant adapter (`internal/backend/qdrant/conformance_test.go`, testcontainers, unmodified, all 13 pass) and the in-memory fake other packages' tests use (`internal/backend/backendtest/conformance_test.go`), so tests built on the fake can't drift from real behavior.

**Proven to catch regressions:** deliberately broken builds failed exactly the subtests you'd expect, then were restored:
- Qdrant applying only its first filter field: failed metadata filter, generation filter, and delete-by-filter.
- Qdrant ignoring the generation filter: failed generation filter and count.
- The fake ignoring the version filter: failed four subtests.

- [x] Suite passes against the Qdrant adapter unmodified.
- [x] Suite is parameterized purely via the `VectorBackend` interface — no Qdrant-specific assumptions leak in.
- [x] Includes: health, namespace setup, upsert, metadata filter, query, generation filter, delete, idempotent upsert, plus count, ID round-trip, AND-semantics delete, ID+filter union delete, and namespace isolation.

