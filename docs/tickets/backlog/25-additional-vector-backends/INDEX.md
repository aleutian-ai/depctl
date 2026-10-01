# Epic: Vector Backends (bring-your-own + embedded)

**Re-scoped (2026-10-01).** ragctl's vector-store positioning is now:

- **If you already run a vector database, ragctl uses it.** The supported set is deliberately Qdrant (shipped), Weaviate (`VEC-011`), and PostgreSQL + pgvector (`VEC-014`), which covers most existing deployments, including the stores under common agent-memory systems: Mem0's library defaults to Qdrant, and its self-hosted server stack uses pgvector. Pair this with epic 65's export connectors to also get cross-agent memory.
- **If you don't run one, ragctl manages a local Qdrant for you** (`vector.managed: true`, the default). That's the one extra service ragctl needs today. It works well, so it stays the default.
- **Longer term, drop the service entirely** with an embedded option (`VEC-015`, plus [epic 30](../30-local-retrieval-mode/INDEX.md)'s Bleve fallback).

History: this epic originally covered four server-backed vendors. On 2026-09-30 it was narrowed to the embedded option, and all four were declined as "another server to run," which is right for a user starting from nothing. On 2026-10-01, Weaviate and pgvector were revived for a different user: one who **already runs** one of them. For that user, support means *fewer* services, not more. Milvus and Chroma stay declined; see [../../completed/64-additional-vector-backend-adapters](../../completed/64-additional-vector-backend-adapters/INDEX.md).

- [VEC-010](VEC-010-backend-conformance-suite.md) — shared conformance test suite every backend must pass (health, namespace setup, upsert, metadata filter, query, generation filter, delete, idempotent upsert). Now a real prerequisite: Weaviate and pgvector make a second and third implementation imminent, the point at which this project's guiding rule says the shared abstraction should be built.
- [VEC-011](VEC-011-weaviate-adapter.md) — Weaviate adapter, for users who already run Weaviate.
- [VEC-014](VEC-014-pgvector-adapter.md) — PostgreSQL + pgvector adapter, for users who already run Postgres with pgvector (including Mem0's self-hosted server stack).
- [VEC-015](VEC-015-sqlite-embedded-backend.md) — embedded SQLite (`sqlite-vec`) backend: one file at `<data-dir>/vectors.db`, no separate process. The longer-term "no vector service at all" path.
- [VEC-016](VEC-016-bring-your-own-qdrant.md) — "use your existing Qdrant" as a documented, real-container-verified path. It already works through config but has never been proven against a Qdrant shared with other tooling.

Suggested order: `VEC-016` (small, docs plus verification, no new code expected) → `VEC-010` → `VEC-014` and `VEC-011` → `VEC-015`.
