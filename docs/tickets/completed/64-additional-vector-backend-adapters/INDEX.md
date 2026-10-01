# Epic: Additional Vector Backend Adapters (Weaviate/Milvus/Chroma/pgvector)

**Declined (2026-09-30).** Split out of `backlog/25-additional-vector-backends` during backlog triage: that epic's two tickets that actually matter for ragctl's near-term priorities (`VEC-010`, the shared conformance suite, and `VEC-015`, embedded SQLite as a genuine zero-install alternative to Qdrant) stayed in `backlog/25-additional-vector-backends`; these four adapter tickets — Weaviate, Milvus, Chroma, PostgreSQL+pgvector — are declined.

Not a technical blocker, a deliberate scoping decision: every one of these is the same weight class as Qdrant itself (a separate service to install and run), so none of them address the actual friction ragctl's local-first users hit — needing a container runtime and a running vector database at all just to try it. `VEC-015` (embedded SQLite) solves that; another server-backed vendor option doesn't. Each ticket already reuses `VEC-010`'s shared conformance suite and adds no new interface surface, so this remains a mechanical "write one more `VectorBackend` implementation" exercise if ragctl ever needs multi-vendor vector-database support later (e.g. to fit an existing team's already-running Weaviate/Milvus/Postgres deployment) — nothing here needs re-scoping.

- [VEC-011](VEC-011-weaviate-adapter.md) — Weaviate adapter via HTTP API.
- [VEC-012](VEC-012-milvus-adapter.md) — Milvus adapter; hybrid search capability is configuration-dependent.
- [VEC-013](VEC-013-chroma-adapter.md) — Chroma adapter via HTTP API; limited hybrid search.
- [VEC-014](VEC-014-pgvector-adapter.md) — PostgreSQL + pgvector adapter; no built-in hybrid/lexical layer.
