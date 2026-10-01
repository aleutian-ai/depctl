# VEC-011: Weaviate adapter

**Epic:** Remaining Vector Backends
**Status:** declined (2026-09-30)
**Depends on:** VEC-010
**Estimated size:** medium

## Goal
Implement `backend.VectorBackend` for Weaviate (https://github.com/weaviate/weaviate, https://docs.weaviate.io/weaviate) using its HTTP API, passing the shared conformance suite.

## Non-goals
- Hybrid search tuning beyond exposing the `HybridSearch` capability flag if Weaviate supports it out of the box.
- GraphQL client generation — plain HTTP/REST is sufficient.

## Simplicity constraints
- Small hand-written HTTP client, matching the Qdrant adapter's approach (VEC-002 explicitly avoids requiring a generated client to keep dependency weight low) — do the same here rather than importing Weaviate's full Go client SDK unless it turns out meaningfully simpler.
- Map `Namespace` to a single Weaviate class per collection; do not build multi-tenant class-per-project logic.

## Design
Package: `internal/backend/weaviate`

```go
type Adapter struct {
    Endpoint string
    HTTPClient *http.Client
}
func (a *Adapter) Name() string { return "weaviate" }
func (a *Adapter) EnsureNamespace(ctx context.Context, ns backend.Namespace) error // create class if absent
func (a *Adapter) Upsert(ctx context.Context, req backend.UpsertRequest) error
func (a *Adapter) Delete(ctx context.Context, req backend.DeleteRequest) error
func (a *Adapter) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error)
func (a *Adapter) Health(ctx context.Context) error
func (a *Adapter) Capabilities(ctx context.Context) (backend.Capabilities, error)
```
Required metadata fields on every object per the namespace model (design spec §45): `ecosystem`, `dependency`, `version`, `generation`, `source_type`, `authority`. These map to Weaviate property filters for version-correct queries.

## Inputs / Outputs
Same as `VectorBackend` interface (VEC-001) — no new inputs/outputs beyond what the interface defines.

## Failure behavior
- Weaviate unreachable → `Health` returns error; upsert/query calls return typed backend errors, not silently swallowed.
- Schema/class creation conflict → idempotent `EnsureNamespace` (check-then-create, tolerate "already exists").

## Tests
- `conformance.RunConformanceSuite` via `testcontainers-go` Weaviate module.
- Insert two versions of the same package; filtered query returns only the requested version (per VEC-002's acceptance bar).

## Acceptance criteria
- [ ] Passes the full VEC-010 conformance suite via testcontainers.
- [ ] Metadata filters support package/ecosystem/version/generation per the namespace model.


## Declined (2026-09-30)

Not being built — a deliberate scoping decision, not a technical blocker. Backlog triage narrowed this epic to the two vector-backend tickets that actually matter for ragctl's own priorities right now: `VEC-010` (the shared conformance suite, backend-agnostic infrastructure) and `VEC-015` (embedded SQLite, a genuine zero-install alternative to Qdrant — see its own ticket). This adapter is the same weight class as Qdrant itself (a separate service to install and run) rather than a lighter alternative, so it does not address the actual friction ragctl's local-first users hit (needing Qdrant + a container runtime at all). Reusing `VEC-010`'s conformance suite, this remains a mechanical "write one more `VectorBackend` implementation" ticket if ever picked up later — no interface work needed.
