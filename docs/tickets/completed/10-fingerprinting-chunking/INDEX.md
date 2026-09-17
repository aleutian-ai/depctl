# Epic: Fingerprinting and Chunking

Establishes deterministic content identity (BLAKE3 fingerprints → object IDs) and splits normalized `KnowledgeObject`s into retrieval-sized `Chunk`s using content-aware strategies (heading-section for Markdown, one-symbol-per-chunk for Go API docs) rather than a single universal token-window policy. Deterministic IDs throughout are what make content reuse (GEN-003) and idempotent backend upserts possible.

## Tickets
- [HASH-001](HASH-001-content-fingerprint-service.md) — Deterministic BLAKE3 fingerprint over source identity + path + normalizer version + content.
- [HASH-002](HASH-002-knowledge-object-ids.md) — Derive `ko_<base32>` object IDs from fingerprints.
- [CHUNK-001](CHUNK-001-chunker-interface.md) — Shared `Chunker` interface and deterministic `chk_<base32>` chunk ID scheme.
- [CHUNK-002](CHUNK-002-markdown-structural-chunker.md) — Heading-section chunking with paragraph-boundary fallback for oversized sections.
- [CHUNK-003](CHUNK-003-symbol-chunker.md) — One-symbol-per-chunk for Go-doc-derived objects.
