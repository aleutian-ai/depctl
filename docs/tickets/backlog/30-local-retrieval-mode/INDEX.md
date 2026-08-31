# Epic: Local-Only Retrieval Mode

Optional but strategically useful: makes `ragctl` usable with zero external infrastructure by defaulting to a local Bleve lexical index when no vector database is configured. A dedicated vector backend remains recommended for semantic retrieval quality.

## Tickets

- [LOCAL-001](LOCAL-001-bleve-lexical-adapter.md) — Implement the `VectorBackend` interface using Bleve for BM25/keyword search.
- [LOCAL-002](LOCAL-002-local-query-fallback.md) — Default to the Bleve backend when no vector backend is configured.
