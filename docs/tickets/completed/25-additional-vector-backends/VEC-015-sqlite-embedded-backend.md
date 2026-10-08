# VEC-015: Embedded vector backend (shipped as plain Go on bbolt, not SQLite)

**Epic:** Remaining Vector Backends
**Status:** done (2026-10-05)
**Depends on:** VEC-010
**Estimated size:** medium

## Goal
Implement `backend.VectorBackend` against an embedded SQLite database using the `sqlite-vec` extension, as a zero-install alternative to Qdrant for a single-user local setup. Surfaced directly by the Grounded Docs comparison: that project uses exactly this combination (`better-sqlite3` + `sqlite-vec`, plus FTS5 for lexical search) as its only storage backend, with a materially lighter first-run footprint than a separate Qdrant process/container. This ticket doesn't have to match its FTS5 hybrid-search layer — just close the "why do I need to run a container to try depctl" gap for a first-time or single-project user.

## Non-goals
- Not a replacement for Qdrant as the recommended/default backend for anything beyond single-user local use — no claim this scales the way a real vector database does under concurrent multi-project load.
- No lexical/FTS5 hybrid search in this ticket — pure vector search only, matching every other `VectorBackend` implementation's `Capabilities.HybridSearch: false` unless a later ticket adds it deliberately.
- Not bundling `sqlite-vec` as a CGo dependency if it meaningfully complicates depctl's own build/cross-compile story — evaluate the pure-Go sqlite-vec loading path (or an alternative pure-Go vector-search library) before committing to a CGo dependency; if CGo turns out unavoidable, that tradeoff needs an explicit decision, not a silent default.

## Simplicity constraints
- Package `internal/backend/sqlitevec`, same interface shape as the Qdrant adapter (VEC-002) and the in-memory test fake — one file per generation's Namespace maps to one SQLite table (or one file, evaluate both), not a new storage architecture.
- Reuses `internal/config`'s existing `vector.managed`-style config pattern rather than inventing new config surface beyond naming the backend and its file path.

## Design
Package: `internal/backend/sqlitevec`

Same interface shape as VEC-011/012/013. `EnsureNamespace` creates (or opens) the backing SQLite file and a `sqlite-vec` virtual table sized to the embedder's `Dimensions()`. Metadata filter fields (ecosystem, dependency, version, generation, source_type, authority) map to ordinary SQLite `WHERE` clauses on companion columns, not a JSON blob scan.

Storage location follows the same `<data-dir>` convention the rest of depctl already uses (`git` cache, bbolt, Badger) — e.g. `<data-dir>/vectors.db` — so a fully local install needs no separate service at all, just this one file alongside the others.

## Inputs / Outputs
Per `VectorBackend` interface (VEC-001).

## Failure behavior
- Corrupted/locked SQLite file → typed error surfaced through the same `Health` check path other backends use.
- `Dimensions()` mismatch against an existing table (embedder changed) → explicit typed error at `EnsureNamespace`, not a silent dimension truncation.

## Tests
- `conformance.RunConformanceSuite` (VEC-010), run against a real temp-file SQLite database, not mocked.
- A concurrent-writer test specifically, since SQLite's single-writer model is the main behavioral difference from Qdrant/Postgres-backed adapters — confirm `internal/daemon`'s single-owner-process model (ADR-011) already avoids concurrent-writer contention here, or document the constraint explicitly if it doesn't.

## Acceptance criteria
- [x] Passes the VEC-010 conformance suite.
- [x] A first-time `depctl init` can select this backend and complete a full sync with zero external services running.
- [x] The CGo-vs-pure-Go tradeoff for loading `sqlite-vec` is explicitly decided and documented, not defaulted into.

## Done (2026-10-05)

**Decision: no SQLite at all.** The three options weighed, with the user, were:
- sqlite-vec via CGo: needs a C toolchain, which complicates cross-compiling and releases.
- sqlite-vec via WASM (`ncruces/go-sqlite3`): a big runtime dependency with fragile version pinning.
- **Plain Go on bbolt (chosen).** bbolt is already a dependency, and the code is the simplest of the three.

**Why no vector index is needed.** Every depctl search is scoped to one dependency version, so an exact brute-force cosine scan over that version's chunks is enough. Measured: about 4 ms for a 2,000-chunk version in a 50,000-point file at 768 dimensions (`BenchmarkQueryOneVersion`). End to end, a search takes about 25–30 ms on both this backend and managed Qdrant, because embedding the query dominates.

`internal/backend/embedded`; select it with `depctl init --vector-backend embedded`, or `vector.backend: embedded` in the config:
- **File.** One file at `<data-dir>/vectors.db`, or `vector.endpoint` set to a file path. A Qdrant `http://` endpoint left over from a switched config is an explicit error.
- **Layout.** One bucket per namespace. Point keys are `ecosystem\0dependency\0version\0generation\0id`, so a version-scoped search is a prefix scan; anything else is a filtered full scan. Exact matching on every field is checked regardless of the prefix.
- **Identity.** A `(generation, id)` index keeps the conformance identity rule: re-upserting the same identity with new metadata replaces the old entry.
- **Writes.** Upserts are one bbolt transaction, so a wrong-sized vector writes nothing (`ErrDimensionMismatch`). A namespace re-ensured with a different dimension is also `ErrDimensionMismatch`.
- **Single writer.** It shares ADR-011's single-owner rule with `control.db`: every call site that builds a backend already runs in the daemon. Concurrent writers inside the daemon are serialized by bbolt (a test runs 8 goroutines × 50 points and gets 400). A second process holding the file gets a clear "in use by another depctl process" error within 2 s, not a hang.
- **No managed container.** The managed Qdrant container never starts for this backend.

Verified end to end in [docs/demos/embedded.md](../../../demos/embedded.md), run as written, with no container started:
- `init --vector-backend embedded`, sync, and search worked;
- two projects on two versions each answered from their own version;
- GC deleted exactly the unreferenced version;
- doctor reported 19 ok.

**Made the default (2026-10-05, the user's decision).** A fresh `depctl init` now writes `vector.backend: embedded`; `depctl init --vector-backend qdrant` gives the previous managed-Qdrant default. Existing configs are untouched. Tests that exercise Qdrant behavior now say so explicitly (`VectorConfig.QdrantDefaults()`) instead of relying on the default.

Found alongside it: doctor printed `vector.endpoint` without redacting it in one check, so a password embedded in a pgvector DSN would have shown there. All three display sites now go through one helper that redacts passwords and shows the embedded file's path.

### Update (2026-10-07): storage layout

The layout described above (one bucket keyed `ecosystem\0dependency\0version\0generation\0id`, plus a `(generation, id)` index) used about 4x the space of its data for the keyword index and 2.5x for vectors. Both stores now keep one bucket per generation, keyed by chunk ID, with the generation's ecosystem, dependency and version stored once, and pack pages full. v0.3.0 files are converted on first open. See `docs/architecture.md`, "Embedded and keyword storage layout", for the measurements.
