# Epic: Local-Only Retrieval Mode

**Done (2026-10-06).** Reconciled to `retrieval.mode: auto | vector | keyword`: fresh installs search by keyword whenever Ollama isn't available, and keyword mode needs no model at all. See both tickets' Done notes.

Optional but strategically useful: makes `depctl` usable with zero external infrastructure by defaulting to a local Bleve lexical index when no vector database is configured. A dedicated vector backend remains recommended for semantic retrieval quality.

**2026-09 note:** a competitive review of Grounded Docs (`docs-mcp-server`) — which ships SQLite+FTS5 lexical search as its baseline, with embeddings as a fully optional upgrade — independently arrived at this exact idea (benchmark local full-text search against the Qdrant+embeddings path; if it handles exact-API-name questions well enough, it lets someone try depctl with zero backend setup). Treat that as external validation this epic is worth prioritizing when local-retrieval-mode work is picked up, not a reason to change its design — see also [VEC-015](../../completed/25-additional-vector-backends/VEC-015-sqlite-embedded-backend.md) for the storage-side counterpart.

## Tickets

- [LOCAL-001](LOCAL-001-bleve-lexical-adapter.md) — **done.** Keyword (BM25) backend in plain Go on bbolt, with an identifier-preserving tokenizer; passes the conformance suite.
- [LOCAL-002](LOCAL-002-local-query-fallback.md) — **done.** `retrieval.mode` (auto by default): keyword search whenever embeddings aren't available, vector backfill once they are.
