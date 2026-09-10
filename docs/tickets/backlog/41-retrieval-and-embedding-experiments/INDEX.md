# Epic: Retrieval and Embedding Experiments

Every ticket in this epic is **evidence-gated**: it either ships a small, low-risk performance/plumbing improvement with today's behavior preserved by default (EMB-004, CHUNK-004), or it explicitly frames itself as an experiment to be run and measured in `context-evals` (the separate eval repository the scratch doc proposes in §15) before any variant becomes a default (CHUNK-005). This follows the scratch doc's §3.5 principle directly:

> Let evals decide which complexity earns permanence. Every attractive retrieval or optimization feature should be framed as a hypothesis... The eval repository exists to answer these questions before the implementation becomes permanent architecture.

## What's deliberately not ticketed here

The scratch doc's §21 ("What not to build now") explicitly lists "SimHash near-dup reuse before profiling" and "embedding-drift dashboards before metrics are validated." Both are honored by omission:

- **SimHash near-duplicate detection** (§9.2) is not ticketed in this epic, on purpose. Before it's worth a ticket, someone needs to measure — on real dependency-version upgrades — the percentage of `KnowledgeObject`s already reused by exact hash (the existing HASH-001 mechanism), the percentage newly created, and the wall-clock cost of re-chunking/re-embedding what's newly created. If that measured waste turns out to be material, a SimHash ticket becomes justified; until then it's speculative complexity (threshold tuning, near-hit indexing, false-positive risk, chunk-diff logic) with no evidence it's needed. §9.3 also notes chunk-level exact reuse may be sufficient even if optimization does turn out to be warranted — simpler than SimHash, worth trying first.
- **Embedding-drift dashboards/comparison tooling** (§10.2) likewise stay out of `internal/embedding` and out of this epic — a model-comparison command belongs in `context-evals` or a thin experimental command, not the core sync path, and only after retrieval/task-outcome metrics exist to say whether embedding-space similarity actually predicts anything worth dashboarding.

## Tickets
- [EMB-004](EMB-004-bounded-embedding-concurrency.md) — `WithConcurrency(n)` option on `internal/embedding/ollama.Client`, default `n=1` (today's behavior unchanged).
- [CHUNK-004](CHUNK-004-token-aware-chunk-sizing.md) — pluggable `TokenEstimator func(string) int` replacing/extending the byte-length cap in `internal/data/chunk/markdown`.
- [CHUNK-005](CHUNK-005-chunk-overlap-experiment.md) — chunk-overlap strategies as an experiment run via `context-evals`, not a default-on feature.

## Non-goals for this epic
- No SimHash/near-duplicate detection (see above).
- No embedding-drift dashboard or production model-comparison command (see above).
- No default-on overlap or structural-neighborhood-expansion behavior shipped without a measured eval result backing the choice (CHUNK-005's own non-goal, restated here at the epic level).
