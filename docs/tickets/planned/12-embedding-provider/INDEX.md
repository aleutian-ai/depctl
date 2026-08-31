# Epic: Embedding Provider

Turns staged chunks into vectors. Establishes a narrow `Embedder` interface, a reference implementation against local Ollama, and a content-hash-keyed cache so re-syncs and unchanged content never pay for redundant embedding calls. Embedding identity (provider/model/dimensions) is tracked explicitly so a model change is always treated as a new replica lineage rather than silently mixed vectors.

## Tickets
- [EMB-001](EMB-001-embedder-interface.md) — `Embedder` interface and `EmbeddingIdentity` metadata type.
- [EMB-002](EMB-002-ollama-embedder.md) — Reference embedder implementation against a local Ollama HTTP endpoint, with batching and retry.
- [EMB-003](EMB-003-embedding-cache.md) — Badger-backed embedding cache keyed by content hash + provider + model.
