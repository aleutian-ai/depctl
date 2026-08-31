# Epic: Vector Backend (Qdrant)

Establishes the vector backend abstraction and its first, reference implementation against Qdrant, chosen because its HTTP API is simple and it's commonly self-hosted. This is the searchable half of a generation: once chunks are embedded (Embedding Provider epic), they're upserted here with mandatory version/generation metadata so retrieval can be constrained to an exact dependency version. Other backends (Weaviate, Milvus, Chroma, pgvector) are deliberately deferred to a later milestone, built only after this adapter and its conformance suite prove the interface.

## Tickets
- [VEC-001](VEC-001-vector-backend-interface.md) — `VectorBackend` interface, `Capabilities`, and the mandatory point-metadata model.
- [VEC-002](VEC-002-qdrant-adapter.md) — Qdrant HTTP adapter: collection setup, upsert, filtered query, delete, health.
- [VEC-003](VEC-003-backend-replica-metadata.md) — bbolt `BackendReplica` bookkeeping tracking replication status/point counts per generation+backend+model.
