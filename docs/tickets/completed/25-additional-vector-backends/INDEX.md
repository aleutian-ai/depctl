# Epic: Vector Backends (bring-your-own + embedded)

**Re-scoped (2026-10-01).** depctl's vector-store positioning is now:

- **If you already run a vector database, depctl uses it.** The supported set is deliberately Qdrant (shipped), Weaviate (`VEC-011`), and PostgreSQL + pgvector (`VEC-014`), which covers most existing deployments, including the stores under common agent-memory systems: Mem0's library defaults to Qdrant, and its self-hosted server stack uses pgvector. Pair this with epic 65's export connectors to also get cross-agent memory.
- **If you don't run one, depctl manages a local Qdrant for you** (`vector.managed: true`, the default). That's the one extra service depctl needs today. It works well, so it stays the default.
- **Longer term, drop the service entirely** with an embedded option (`VEC-015`, plus [epic 30](../30-local-retrieval-mode/INDEX.md)'s Bleve fallback).

History: this epic originally covered four server-backed vendors. On 2026-09-30 it was narrowed to the embedded option, and all four were declined as "another server to run," which is right for a user starting from nothing. On 2026-10-01, Weaviate and pgvector were revived for a different user: one who **already runs** one of them. For that user, support means *fewer* services, not more. Milvus and Chroma stay declined; see [../../completed/64-additional-vector-backend-adapters](../../completed/64-additional-vector-backend-adapters/INDEX.md).

- [VEC-010](VEC-010-backend-conformance-suite.md) — **done (2026-10-05).** `internal/backend/conformance.Run`: 15 subtests every backend must pass, running against real Qdrant, pgvector and Weaviate, and the in-memory fake, and proven to catch deliberately broken filters.
- [VEC-011](VEC-011-weaviate-adapter.md) — **done (2026-10-05).** Weaviate adapter (`vector.backend: weaviate`), for users who already run Weaviate. Verified end to end alongside another app's collection, with API-key auth on. It added conformance subtest 15: filters must match whole values, not tokens.
- [VEC-014](VEC-014-pgvector-adapter.md) — **done (2026-10-05).** PostgreSQL + pgvector adapter (`vector.backend: pgvector`), for users who already run Postgres with pgvector (including Mem0's self-hosted server stack). Verified end to end in a shared database. It surfaced a point-identity rule the suite didn't check (now subtest 14) and a never-read `vector.api_key_env` for Qdrant (fixed).
- [VEC-015](VEC-015-sqlite-embedded-backend.md) — **done (2026-10-05).** Embedded backend (`depctl init --vector-backend embedded`): one bbolt file at `<data-dir>/vectors.db`, with exact cosine search scoped to one dependency version. No service and no container. It's plain Go rather than SQLite: no CGo and no new dependency, decided with the user.
- [VEC-016](VEC-016-bring-your-own-qdrant.md) — **done (2026-10-01).** "Use your existing Qdrant" is verified against a real server shared with another collection: sync, rebuild, and every GC path left the neighbor byte-identical, and `managed: false` never starts a container. No code change needed. The run also surfaced three unrelated bugs (version changes don't trigger rebuilds; a failed sync can strand a dependency; vector readiness never rechecks), recorded in the ticket.

Suggested order: `VEC-016` (done) → `VEC-010` (done) → `VEC-014` (done) → `VEC-011` (done) → `VEC-015` (done). **Epic complete (2026-10-05).**
