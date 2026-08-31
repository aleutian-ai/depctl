# GEN-003: Content reuse via fingerprint lookup

**Epic:** Generation Builder
**Status:** planned
**Depends on:** HASH-001, GEN-002
**Estimated size:** small

## Goal
Before storing a newly normalized `KnowledgeObject`, look up its content hash in Badger; if an object with that hash already exists, reuse the stored payload and merely associate it with the new generation instead of duplicating it.

## Non-goals
- Does not touch embedding reuse (EMB-003 handles the embedding cache separately, keyed similarly but distinct from object reuse).

## Simplicity constraints
- Reuse check is a single Badger `Get` on `hash/<content-hash>` returning the existing object ID — no separate dedup index structure or background compaction job.
- Track `objects_reused` / `objects_created` as simple in-memory counters returned from `Build`, persisted into the generation manifest — not a full metrics subsystem (that's OBS-* later).

## Design
- Extend `internal/data/generation.Build` (GEN-002): after normalizing each object, compute `ContentHash` (HASH-001) and check `hash/<content-hash>` in Badger.
  - Hit: read existing `obj/<object-id>`, add a generation-membership reference (e.g. append generation ID to a small membership list, or write `chunk/<generation-id>/...` pointing at the existing object) without rewriting the object payload.
  - Miss: write new `obj/<object-id>` and `hash/<content-hash> -> object-id` index entry.
- Add `objects_reused` / `objects_created` fields to the generation manifest JSON.

## Inputs / Outputs
- Input: a normalized `KnowledgeObject` with computed content hash.
- Output: either a reused reference or a newly stored object; manifest counters updated.

## Failure behavior
- Hash index write failure: fail the generation build (same failure path as GEN-002) rather than silently proceeding without dedup, to avoid unbounded duplicate growth.

## Tests
- Building two generations from unchanged source content shows `objects_reused` == total object count on the second build, `objects_created` == 0.
- Changing one file's content produces exactly one new object; the rest are reused.

## Acceptance criteria
- [x] Repeated generation build over unchanged sources creates zero new Badger objects.
- [x] Manifest exposes `objects_reused` and `objects_created` counts.
