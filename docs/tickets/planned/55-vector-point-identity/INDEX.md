# Epic: Vector Point Identity

Found by COORD-003's 3/5/7 concurrency benchmark. A Qdrant point ID was derived from a chunk's content only (`chunk.ChunkID` → `qdrant.pointID`), so two generations holding byte-identical content (sibling modules of one monorepo, or a file unchanged between two versions) wrote to the *same* point, and the later upsert overwrote the earlier generation's `dependency`/`version`/`generation` payload.

Measured on the terraform fixture (N=7, cold): **99 of 141 dependencies that reported `OK` had zero points in Qdrant.** Validation failures (`query returned no results within generation ...`) were only the visible tip, seen when a sibling overwrote points between a dependency's upsert and its own validation. Sequential syncs lose the points silently instead. Chunks that failed validation for `cloud.google.com/go/artifactregistry`, `cloudbuild`, `clouddms` etc. were all found owned by `cloud.google.com/go/workflows`.

## Tickets
- [x] [POINT-001](POINT-001-generation-scoped-point-ids.md) — point ID now includes the generation. Fixed and verified live (0 of 167 `OK` dependencies empty; validation failures 18 → 0 at N=5).
- [ ] [POINT-002](POINT-002-shared-content-storage-cost.md) — options 2 and version-exact refs done and measured on a 13-dependency subset; full-scale re-measurement and a migration path for pre-fix generations remain open.
- [x] [POINT-003](POINT-003-detect-empty-active-generations.md) — **done.** `ragctl doctor` now flags an ACTIVE generation with zero points despite a non-empty manifest — the exact pre-POINT-001 failure class, for a collection populated before that fix shipped. Required a new `VectorBackend.Count` method (the interface's first addition past its original deliberate six), live-verified against a real Qdrant container. Repair is the existing `ragctl sync --force`, already proven throughout this session.

## Status

**Epic stays in `planned/`** — POINT-002's own remaining scope (full-scale re-measurement, a migration path for generations built before POINT-001 shipped) is real and untouched.
