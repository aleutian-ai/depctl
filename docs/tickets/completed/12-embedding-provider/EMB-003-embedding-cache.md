# EMB-003: Embedding cache

**Epic:** Embedding Provider
**Status:** done
**Depends on:** STORE-003, EMB-002
**Estimated size:** small

## Goal
Cache embeddings in Badger keyed by chunk content hash + provider + model + normalization settings, so unchanged chunks are never re-embedded across generations.

## Non-goals
- No cross-machine/shared embedding cache — this is purely local Badger state.

## Simplicity constraints
- Cache key is a single deterministic string (hash of hash+provider+model+normalization) — no separate index structures beyond the one Badger key lookup.
- Do not cache partial/failed embed attempts; only successful vectors are written.

## Design
- Package: `internal/embedding/cache` wrapping any `embedding.Embedder`.
- Cache key: `embedcache/<chunk-content-hash>/<provider>/<model-id>` — a distinct prefix from STORE-003's `embed/<generation-id>/<provider>/<chunk-id>`, which stores per-replica embedding *metadata* (generation-scoped, via `PutEmbeddingMetadata`/`GetEmbeddingMetadata`). This cache is content-addressed on purpose so it survives across generations, not scoped to one. Use the same `PutEmbeddingMetadata`/`GetEmbeddingMetadata` methods from STORE-003 with this key shape — no new `Store` methods needed.
- `type CachingEmbedder struct { inner embedding.Embedder; store *badger.Store }` implementing `embedding.Embedder`:
  - On `Embed`, split input texts into cache hits/misses by key lookup.
  - Call `inner.Embed` only for misses; write results to cache; merge with hits preserving original order.
- Track `embeddings_reused` / `embeddings_generated` counters, added to the generation manifest (mirrors GEN-003's `objects_reused`/`objects_created` pattern).

## Inputs / Outputs
- Input: chunk texts + their content hashes (chunk ID already encodes content, per CHUNK-001).
- Output: vectors, with cache hit/miss counts.

## Failure behavior
- Cache read/write errors: log and fall through to calling the inner embedder directly for that chunk (never let a cache failure block embedding) — but do not attempt to write to a broken cache repeatedly in the same call (skip caching for the rest of that batch).

## Tests
- Embedding the same chunk twice results in exactly one call to the inner embedder.
- Changing model ID for the same chunk content results in a cache miss (no cross-model reuse).
- Manifest counters reflect reused vs generated correctly.

## Acceptance criteria
- [x] Second identical generation build performs zero live embedding calls.
- [x] Switching embedding model produces fresh embeddings, not stale cached ones.

## Post-implementation note
Verified directly against `CachingEmbedder` (`TestEmbedSameChunkTwiceCallsInnerOnce`, `TestModelChangeIsCacheMiss`), not through an actual `generation.Build` run — `Build` (GEN-002) doesn't call an embedder at all yet, embedding is still a separate, unwired stage per GEN-002's own non-goals. `Counts()` exposes `reused`/`generated` for whichever future step (part of the vector-backend replication epic, VEC-*) drives embedding during a build and feeds them into the generation manifest's `objects_reused`-style counters — not added to `generation.Manifest` in this ticket since there's no caller yet.
