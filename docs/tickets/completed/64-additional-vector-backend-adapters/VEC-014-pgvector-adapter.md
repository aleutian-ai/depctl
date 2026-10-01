# VEC-014: pgvector adapter

**Epic:** Remaining Vector Backends
**Status:** declined (2026-09-30)
**Depends on:** VEC-010
**Estimated size:** medium

## Goal
Implement `backend.VectorBackend` for PostgreSQL + pgvector (https://github.com/pgvector/pgvector), tested with PostgreSQL + pgvector via `testcontainers-go`, passing the shared conformance suite. Per the design spec, pgvector needs an external lexical layer for hybrid search — do not fake hybrid support.

## Non-goals
- Building a lexical/BM25 layer on top of Postgres for hybrid search — out of scope; `Capabilities.HybridSearch` stays false unless a real implementation is added later.
- Connection pooling tuning beyond a sane default `pgxpool` configuration.

## Simplicity constraints
- Use `pgx`/`pgxpool` (the standard, well-maintained Postgres driver) — do not add a full ORM.
- One table per `Namespace` (or one table with a namespace/collection column — pick the simpler of the two; a single shared table with an indexed `namespace` column is likely less schema-migration churn than dynamic per-namespace tables).
- Required metadata fields stored as regular indexed columns (ecosystem, dependency, version, generation, source_type, authority) rather than a JSONB blob, so filtering stays simple `WHERE` clauses.

## Design
Package: `internal/backend/pgvector`

```go
type Adapter struct {
    Pool *pgxpool.Pool
}
```
Same interface shape as VEC-011/012/013. `EnsureNamespace` runs `CREATE EXTENSION IF NOT EXISTS vector` + `CREATE TABLE IF NOT EXISTS ... (id text, embedding vector(N), ecosystem text, dependency text, version text, generation text, source_type text, authority int)` with an index on the embedding column (e.g. HNSW or IVFFlat, whichever pgvector's stable feature set supports at implementation time) and btree indexes on filter columns.

## Inputs / Outputs
Per `VectorBackend` interface (VEC-001).

## Failure behavior
- Extension not installed / insufficient privileges → `EnsureNamespace` returns a typed error naming the missing extension.
- Dimension mismatch on upsert (embedding length ≠ column dimension) → typed error before the write.

## Tests
- `conformance.RunConformanceSuite` via `testcontainers-go` Postgres+pgvector module.
- Dimension mismatch rejected with a clear error.
- Filtered query returns only the requested ecosystem/version.

## Acceptance criteria
- [ ] Passes VEC-010 conformance suite.
- [ ] `Capabilities.HybridSearch` is false; `KeywordSearch` is false unless explicitly implemented.
- [ ] Extension bootstrap is idempotent across repeated `EnsureNamespace` calls.


## Declined (2026-09-30)

Not being built — a deliberate scoping decision, not a technical blocker. Backlog triage narrowed this epic to the two vector-backend tickets that actually matter for ragctl's own priorities right now: `VEC-010` (the shared conformance suite, backend-agnostic infrastructure) and `VEC-015` (embedded SQLite, a genuine zero-install alternative to Qdrant — see its own ticket). This adapter is the same weight class as Qdrant itself (a separate service to install and run) rather than a lighter alternative, so it does not address the actual friction ragctl's local-first users hit (needing Qdrant + a container runtime at all). Reusing `VEC-010`'s conformance suite, this remains a mechanical "write one more `VectorBackend` implementation" ticket if ever picked up later — no interface work needed.
