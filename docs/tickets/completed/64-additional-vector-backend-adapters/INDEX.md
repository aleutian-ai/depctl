# Epic: Additional Vector Backend Adapters (Milvus/Chroma)

**Declined (2026-09-30).** Split out of `backlog/25-additional-vector-backends` during backlog triage, which declined four server-backed adapters (Weaviate, Milvus, Chroma, PostgreSQL + pgvector) as "the same weight class as Qdrant: another separate service to install and run."

**Weaviate (`VEC-011`) and pgvector (`VEC-014`) were revived on 2026-10-01** and moved back to [backlog/25](../25-additional-vector-backends/INDEX.md). The decline reasoning holds for a user starting from nothing, but missed users who *already run* one of those databases. For them, support means ragctl adds no vector service at all. Those two plus the shipped Qdrant adapter are the deliberate supported set, covering most existing deployments.

The two tickets below stay declined. Neither came up as something ragctl's users already run, and each remains a mechanical "write one more `VectorBackend` implementation" ticket against `VEC-010`'s conformance suite if that changes.

- [VEC-012](VEC-012-milvus-adapter.md) — Milvus adapter; hybrid search capability is configuration-dependent.
- [VEC-013](VEC-013-chroma-adapter.md) — Chroma adapter via HTTP API; limited hybrid search.
