# Epic: Local-Only Retrieval Mode

Optional but strategically useful: makes `ragctl` usable with zero external infrastructure by defaulting to a local Bleve lexical index when no vector database is configured. A dedicated vector backend remains recommended for semantic retrieval quality.

**2026-09 note:** a competitive review of Grounded Docs (`docs-mcp-server`) — which ships SQLite+FTS5 lexical search as its baseline, with embeddings as a fully optional upgrade — independently arrived at this exact idea (benchmark local full-text search against the Qdrant+embeddings path; if it handles exact-API-name questions well enough, it lets someone try ragctl with zero backend setup). Treat that as external validation this epic is worth prioritizing when local-retrieval-mode work is picked up, not a reason to change its design — see also [VEC-015](../25-additional-vector-backends/VEC-015-sqlite-embedded-backend.md) for the storage-side counterpart.

## Tickets

- [LOCAL-001](LOCAL-001-bleve-lexical-adapter.md) — Implement the `VectorBackend` interface using Bleve for BM25/keyword search.
- [LOCAL-002](LOCAL-002-local-query-fallback.md) — Default to the Bleve backend when no vector backend is configured.
