# VEC-015: SQLite (sqlite-vec) embedded vector backend

**Epic:** Remaining Vector Backends
**Status:** planned
**Depends on:** VEC-010
**Estimated size:** medium

## Goal
Implement `backend.VectorBackend` against an embedded SQLite database using the `sqlite-vec` extension, as a zero-install alternative to Qdrant for a single-user local setup. Surfaced directly by the Grounded Docs comparison: that project uses exactly this combination (`better-sqlite3` + `sqlite-vec`, plus FTS5 for lexical search) as its only storage backend, with a materially lighter first-run footprint than a separate Qdrant process/container. This ticket doesn't have to match its FTS5 hybrid-search layer — just close the "why do I need to run a container to try ragctl" gap for a first-time or single-project user.

## Non-goals
- Not a replacement for Qdrant as the recommended/default backend for anything beyond single-user local use — no claim this scales the way a real vector database does under concurrent multi-project load.
- No lexical/FTS5 hybrid search in this ticket — pure vector search only, matching every other `VectorBackend` implementation's `Capabilities.HybridSearch: false` unless a later ticket adds it deliberately.
- Not bundling `sqlite-vec` as a CGo dependency if it meaningfully complicates ragctl's own build/cross-compile story — evaluate the pure-Go sqlite-vec loading path (or an alternative pure-Go vector-search library) before committing to a CGo dependency; if CGo turns out unavoidable, that tradeoff needs an explicit decision, not a silent default.

## Simplicity constraints
- Package `internal/backend/sqlitevec`, same interface shape as the Qdrant adapter (VEC-002) and the in-memory test fake — one file per generation's Namespace maps to one SQLite table (or one file, evaluate both), not a new storage architecture.
- Reuses `internal/config`'s existing `vector.managed`-style config pattern rather than inventing new config surface beyond naming the backend and its file path.

## Design
Package: `internal/backend/sqlitevec`

Same interface shape as VEC-011/012/013. `EnsureNamespace` creates (or opens) the backing SQLite file and a `sqlite-vec` virtual table sized to the embedder's `Dimensions()`. Metadata filter fields (ecosystem, dependency, version, generation, source_type, authority) map to ordinary SQLite `WHERE` clauses on companion columns, not a JSON blob scan.

Storage location follows the same `<data-dir>` convention the rest of ragctl already uses (`git` cache, bbolt, Badger) — e.g. `<data-dir>/vectors.db` — so a fully local install needs no separate service at all, just this one file alongside the others.

## Inputs / Outputs
Per `VectorBackend` interface (VEC-001).

## Failure behavior
- Corrupted/locked SQLite file → typed error surfaced through the same `Health` check path other backends use.
- `Dimensions()` mismatch against an existing table (embedder changed) → explicit typed error at `EnsureNamespace`, not a silent dimension truncation.

## Tests
- `conformance.RunConformanceSuite` (VEC-010), run against a real temp-file SQLite database, not mocked.
- A concurrent-writer test specifically, since SQLite's single-writer model is the main behavioral difference from Qdrant/Postgres-backed adapters — confirm `internal/daemon`'s single-owner-process model (ADR-011) already avoids concurrent-writer contention here, or document the constraint explicitly if it doesn't.

## Acceptance criteria
- [ ] Passes the VEC-010 conformance suite.
- [ ] A first-time `ragctl init` can select this backend and complete a full sync with zero external services running.
- [ ] The CGo-vs-pure-Go tradeoff for loading `sqlite-vec` is explicitly decided and documented, not defaulted into.
