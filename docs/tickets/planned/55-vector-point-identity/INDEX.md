# Epic: Vector Point Identity

Found by COORD-003's 3/5/7 concurrency benchmark. A Qdrant point ID was derived from a chunk's content only (`chunk.ChunkID` → `qdrant.pointID`), so two generations holding byte-identical content (sibling modules of one monorepo, or a file unchanged between two versions) wrote to the *same* point, and the later upsert overwrote the earlier generation's `dependency`/`version`/`generation` payload.

Measured on the terraform fixture (N=7, cold): **99 of 141 dependencies that reported `OK` had zero points in Qdrant.** Validation failures (`query returned no results within generation ...`) were only the visible tip, seen when a sibling overwrote points between a dependency's upsert and its own validation. Sequential syncs lose the points silently instead. Chunks that failed validation for `cloud.google.com/go/artifactregistry`, `cloudbuild`, `clouddms` etc. were all found owned by `cloud.google.com/go/workflows`.

## Tickets
- [x] [POINT-001](POINT-001-generation-scoped-point-ids.md) — point ID now includes the generation. Fixed and verified live (0 of 167 `OK` dependencies empty; validation failures 18 → 0 at N=5).
- [ ] [POINT-002](POINT-002-shared-content-storage-cost.md) — the fix stores ~10x what deduplication would (measured: 91% of rows are duplicates). Options below; needs a decision.
- [ ] [POINT-003](POINT-003-detect-empty-active-generations.md) — find and repair already-synced generations that are ACTIVE but have no points.
