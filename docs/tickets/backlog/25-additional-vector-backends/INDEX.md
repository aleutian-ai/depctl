# Epic: Remaining Vector Backends

Add vector backend adapters beyond the reference Qdrant implementation (VEC-002), each conforming to the same `VectorBackend` interface (VEC-001) and the shared conformance test suite. Only begin after the conformance suite exists — build one reference backend, write conformance tests, only then add more implementations (implementation plan §3.3).

- [VEC-010](VEC-010-backend-conformance-suite.md) — shared conformance test suite every backend must pass: health, namespace setup, upsert, metadata filter, query, generation filter, delete, idempotent upsert.
- [VEC-011](VEC-011-weaviate-adapter.md) — Weaviate adapter via HTTP API.
- [VEC-012](VEC-012-milvus-adapter.md) — Milvus adapter; hybrid search capability is configuration-dependent.
- [VEC-013](VEC-013-chroma-adapter.md) — Chroma adapter via HTTP API; limited hybrid search.
- [VEC-014](VEC-014-pgvector-adapter.md) — PostgreSQL + pgvector adapter; no built-in hybrid/lexical layer.
- [VEC-015](VEC-015-sqlite-embedded-backend.md) — Embedded SQLite (`sqlite-vec`) backend for a zero-install single-user setup, surfaced by comparing against Grounded Docs' SQLite-only storage model.
