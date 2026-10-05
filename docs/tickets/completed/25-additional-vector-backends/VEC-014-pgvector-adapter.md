# VEC-014: pgvector adapter

**Epic:** Vector Backends (bring-your-own + embedded)
**Status:** done (2026-10-05; revived 2026-10-01; declined 2026-09-30 — see the notes at the end)
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
- [x] Passes VEC-010 conformance suite.
- [x] `Capabilities.HybridSearch` is false; `KeywordSearch` is false unless explicitly implemented.
- [x] Extension bootstrap is idempotent across repeated `EnsureNamespace` calls.


## Done (2026-10-05)

`internal/backend/pgvector`, on `pgx/v5` + `pgxpool`. Selected with `vector.backend: pgvector`; `vector.endpoint` is the Postgres connection string and `vector.api_key_env` names the env var holding the password.

How it was reconciled against the sketch above:
- **One table per namespace, not a shared table.** The namespace is already ragctl's per-install name (`ragctl-<random>`, the same isolation rule as the Qdrant collection), so a per-namespace table *is* "ragctl's own table" in a shared database. It needs no extra column and no schema migration.
- **The primary key is `(generation, id)`, not `id`.** Chunk IDs are content-derived, so two versions of a dependency share IDs for unchanged chunks. Keying on `id` alone let the newer version's upsert relabel the older version's rows. The end-to-end run caught this: a second project on v1.5.0 left the v1.6.0 project with 12 of its 84 rows, and its search lost `New`/`NewRandom`. Qdrant and the test fake already keyed on (generation, id), but nothing stated it. Now `backend.Point` documents it, and the conformance suite checks it (`SameIDInTwoGenerationsIsTwoPoints`, now 14 subtests). The id-only key fails that check.
- **Wrong-dimension writes fail inside Postgres**, in a transaction, so the whole batch rolls back. There's no separate pre-check, because the `vector(N)` column already enforces it. A namespace re-ensured with a different dimension returns `ErrDimensionMismatch`.
- **Cosine only** (HNSW `vector_cosine_ops`), which is the only distance ragctl uses. Anything else is rejected.
- **One pool per database per process**, because ragctl builds a backend per operation.

Verified with the shared conformance suite plus four pgvector-specific tests against a real `pgvector/pgvector:pg17` container. End to end, a sandboxed ragctl ran against a Postgres holding another app's table (the same image Mem0's server stack uses). Results:
- sync and search worked;
- two projects on two versions each answered from their own version (84 + 81 rows);
- GC deleted exactly the unreferenced version's rows;
- the other table was untouched throughout;
- doctor reported a wrong or missing password clearly, without printing it.

This is [docs/demos/pgvector.md](../../../demos/pgvector.md), run as written. It was not run against a live Mem0 server sharing the database; the foreign table stands in for one.

Found alongside it: `vector.api_key_env` was documented for Qdrant but never read, so a key-protected Qdrant could not be used. ragctl now sends it as `api-key`, and with a key set, Health checks `/collections` rather than `/healthz` (which Qdrant leaves open), so doctor no longer reports a wrong key as healthy. Verified against a real Qdrant started with `QDRANT__SERVICE__API_KEY`. The managed container is Qdrant-only; `managed: true` with `backend: pgvector` never starts one.

## Revived (2026-10-01)

Same reasoning as `VEC-011`'s revival. The decline below judged pgvector as "another server to run." It missed users who **already run** Postgres with pgvector. The concrete case: Mem0's own self-hosted server stack deploys pgvector by default, so a Mem0 user already has one running. For them, this adapter means ragctl adds no extra vector service at all. It also pairs with `MEM0-001` (epic 65): ragctl's index and the user's Mem0 can share the same Postgres instance in separate tables, and the export connector still provides the cross-agent memory benefit. Part of the deliberate supported set: Qdrant, Weaviate, pgvector.

Implementation notes for whoever picks this up:
- Use ragctl's own table (or own schema), never Mem0's or any other tool's. Make the table name per-install, following the same isolation rule as the Qdrant adapter's collection name (`SAFE-001`).
- `CREATE EXTENSION IF NOT EXISTS vector` may need privileges a shared Postgres doesn't grant. If the extension is already installed, skip the create rather than failing. Fail with the typed "missing extension" error only when it's genuinely absent.
- Verify against a real Postgres + pgvector under Podman. Ideally that's the actual Mem0 server stack's Postgres, to prove the shared-instance case rather than assert it.

## Declined (2026-09-30)

Not being built — a deliberate scoping decision, not a technical blocker. Backlog triage narrowed this epic to the two vector-backend tickets that actually matter for ragctl's own priorities right now: `VEC-010` (the shared conformance suite, backend-agnostic infrastructure) and `VEC-015` (embedded SQLite, a genuine zero-install alternative to Qdrant — see its own ticket). This adapter is the same weight class as Qdrant itself (a separate service to install and run) rather than a lighter alternative, so it does not address the actual friction ragctl's local-first users hit (needing Qdrant + a container runtime at all). Reusing `VEC-010`'s conformance suite, this remains a mechanical "write one more `VectorBackend` implementation" ticket if ever picked up later — no interface work needed.
