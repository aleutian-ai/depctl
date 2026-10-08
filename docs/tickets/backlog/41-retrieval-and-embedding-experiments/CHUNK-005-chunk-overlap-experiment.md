# CHUNK-005: Chunk overlap experiment

**Epic:** Retrieval and Embedding Experiments
**Status:** planned
**Depends on:** CHUNK-004 (token-aware sizing — overlap is measured in the same unit), the `context-evals` repository (scratch doc §15; not yet created as of this ticket)
**Estimated size:** medium (mostly eval harness work, not `depctl` core)

## Goal
Measure whether chunk overlap — and, separately, structural-neighborhood-expansion (returning a hit's parent section/adjacent siblings rather than duplicating bytes across a boundary) — improves retrieval quality for developer documentation, before either becomes a default `depctl` behavior. This ticket is explicitly framed as **an experiment to run in `context-evals`**, per the scratch doc's §8.3 and its governing §3.5 principle ("let evals decide which complexity earns permanence"). It is not a request to ship an overlap feature.

Hypothesis under test, stated in the scratch doc's own words: **"does semantic structure beat redundant byte overlap for developer documentation?"**

## Non-goals
- **Do not ship any of the compared strategies as depctl's default chunking behavior as part of this ticket.** A measured result is a prerequisite for that decision, not an outcome guaranteed by running the experiment — "no overlap wins" is an acceptable, useful result.
- No production `depctl` config surface for selecting an overlap strategy (e.g. no `chunk.overlap_mode` YAML key) until a variant actually wins a measured comparison — adding configurability for an unproven feature is exactly the kind of premature complexity §3.5 warns against.
- No new user-facing CLI command in `depctl` itself — the comparison harness lives in `context-evals`, a separate repository (scratch doc §15.2's "Independence rule": eval tooling stays decoupled from `depctl` core so it can evaluate `depctl` from the outside).
- Structural neighborhood expansion here means only "return a hit's parent section + adjacent siblings" (scratch doc §8.4) — it does not include symbol/example cross-referencing (also mentioned in §8.4's diagram) unless a follow-up scopes that in after this one lands.

## Simplicity constraints
- Three variants only, nothing more: no-overlap (today's `internal/data/chunk/markdown.Chunker` behavior, unchanged), fixed-byte/token overlap (a small, bounded addition to the chunker — reuse CHUNK-004's `TokenEstimator` to express overlap size in the same unit as chunk size), and structural-neighborhood-expansion (a query-time behavior: expand a matched chunk's evidence with its parent section/siblings via `domain.Chunk.Metadata["heading_path"]`, which `internal/data/chunk/markdown.Chunker.Chunk` already populates today — no chunker change needed for this variant, only a query-serving change gated behind the eval harness, not wired into `internal/query.Service` by default).
- The fixed-overlap variant, if implemented as a `depctl`-side code change to make the comparison possible, must be additive and off-by-default (an `Option` on `Chunker`, following CHUNK-004's own pattern) — never a change to `Chunker`'s existing zero-option behavior.
- Reuse `internal/query.Service.SearchKnowledge` and existing `domain.Chunk`/`ResultChunk` shapes for feeding results into the eval harness — no new retrieval-serving type invented in `depctl` for this experiment alone.

## Design
This ticket's deliverable is primarily an eval harness in `context-evals` (out of this repo's tree), plus the minimum `depctl`-side plumbing needed to produce comparable inputs:

1. **`depctl`-side (this repo):**
   - `internal/data/chunk/markdown.Chunker` gains a `WithOverlap(n int, estimator TokenEstimator)` `Option` (same functional-option shape as CHUNK-004's `WithTokenLimit`) that duplicates the trailing `n`-token slice of a chunk at the start of the next chunk within the same section — purely additive, `n <= 0` (default) preserves today's exact no-overlap output.
   - `internal/query.Service` gains no new default behavior; a neighborhood-expansion helper (`ExpandNeighborhood(ctx, chunk domain.Chunk) ([]domain.Chunk, error)`, using `Chunk.Metadata["heading_path"]` plus `DataStore.ListGenerationChunks` — already in `query.DataStore`'s interface — to find same-section siblings) is added as an explicitly-opt-in method the eval harness calls directly, not one `SearchKnowledge` invokes automatically.
2. **`context-evals`-side (separate repo, referenced not built here):** a comparison suite running the same corpus/query set (scratch doc §16's eval matrix, particularly H2: structure hypothesis) through all three variants, reporting per §18.2 ("Retrieval" evaluation layer): retrieval precision/recall at K, whether the correct evidence was present in the returned set, and downstream task-outcome delta (§18.4) where feasible.

## Inputs / Outputs
- Input: a fixed evaluation corpus + query set in `context-evals` (not created by this ticket; assumed to exist per that repo's own setup, or stood up as a prerequisite task there).
- Output: a written comparison result (in `context-evals`, not this repo) stating, per variant, retrieval-quality deltas and whether the "semantic structure beats redundant overlap" hypothesis held. This ticket's `depctl`-side acceptance criteria stop at "the three variants are producible and comparable" — the actual comparison run and its numbers are `context-evals` scope.

## Failure behavior
- If `context-evals` doesn't exist yet when this ticket is picked up, standing it up (or at minimum a query/corpus fixture sufficient for this one comparison) is a blocking prerequisite, not something this ticket route around by skipping straight to shipping a default in `depctl`.
- An inconclusive result (no variant clearly wins) is a valid, complete outcome — it means "no overlap" (today's behavior) stays the default by default, not that the experiment failed.

## Tests
- `WithOverlap(0, ...)` (default) produces byte-for-byte identical output to `Chunker` without the option, on the existing `internal/data/chunk/markdown/testdata` fixtures.
- `WithOverlap(n, estimator)` with `n > 0` produces chunks where the trailing ~`n`-token slice of chunk `i` reappears at the start of chunk `i+1` within the same section, verified against a small fixture with a section long enough to split.
- `ExpandNeighborhood` returns the expected sibling set for a fixture `KnowledgeObject` with multiple sections at the same heading depth, using only `Chunk.Metadata["heading_path"]` and `ListGenerationChunks` — no new storage read path.

## Acceptance criteria
- [ ] `WithOverlap` option added to `internal/data/chunk/markdown.Chunker`, off by default, additive only.
- [ ] `query.Service.ExpandNeighborhood` (or equivalently-scoped helper) added as an explicitly-invoked method, never called automatically by `SearchKnowledge`.
- [ ] No production config surface or CLI flag added for selecting an overlap/expansion strategy.
- [ ] The comparison itself (corpus, queries, results, hypothesis verdict) is tracked and run in `context-evals`, referenced from this ticket's completion note rather than duplicated into this repo's docs.
- [ ] Explicit non-goal restated: no variant becomes `depctl`'s default chunking/retrieval behavior as a side effect of this ticket landing.
