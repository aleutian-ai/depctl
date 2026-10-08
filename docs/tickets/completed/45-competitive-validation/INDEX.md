# Epic: Competitive Validation (repository-state authority)

Forced by a 2026-09 deep-dive comparison against Grounded Docs (`docs-mcp-server`), an open-source, locally-hosted, MIT-licensed documentation MCP server that turned out to be substantially more mature than initially represented: real job persistence, hybrid lexical/vector search over SQLite+FTS5+sqlite-vec, broad document/package ingestion, and its own retrieval benchmark (MRR/Recall@K/nDCG against Context7).

That comparison converged on one specific, testable claim as depctl's actual differentiator, not "better RAG in general":

> Grounded Docs manages versioned documentation collections. depctl manages the relationship between a repository's *resolved dependency state* and the knowledge an agent is permitted to retrieve.

This epic exists to turn that claim into evidence rather than positioning copy — deterministic, in-repo, depctl-owned tests that prove (or disprove) the specific lifecycle guarantees the rest of the codebase already claims to make (atomic promotion, no silent cross-version fallback, project-reference-aware retention), plus the fixtures a later comparative benchmark would need. It does **not** build a cross-system benchmark harness itself — see the note below.

## Relationship to epic 26 (evaluation framework)

Epic 26's own INDEX already flags an unresolved split: deterministic, depctl-owned correctness gates (EVAL-002/005) stay in this repo; comparative retrieval-quality benchmarking across systems (EVAL-001/003/004 — Precision@K/Recall@K/MRR/nDCG, a CLI/reporting surface) was proposed to move to a separate `context-evals` repo that grades depctl only through its public CLI/MCP surface, never importing internal packages.

This competitive review reaffirms that split rather than reopening it: **a depctl adapter for Grounded Docs' benchmark runner, and the five-scenario depctl-vs-Grounded-Docs head-to-head test plan, belong in `context-evals` (or Grounded Docs' own benchmark repo, which already supports adding a provider), not here.** `context-evals` doesn't exist yet; nothing in this epic is blocked on it existing, and nothing here should grow into building it. VALID-004's fixture is deliberately written to be reusable by that future harness without depctl depending on it.

**2026-09: pulled forward from backlog and shipped in full.** A design review of depctl's current state flagged VALID-001 as the single highest-priority gap (the most load-bearing correctness claim in the system, unverified by any test), so this epic moved from `backlog/` to `planned/` to be worked rather than deferred — and all five tickets landed in one pass.

## Tickets

- [VALID-001](VALID-001-atomic-promotion-under-failure.md) — **done.** Prove the "never silently serve a stale/mismatched version" invariant under an injected mid-build failure. Confirmed: the safety property holds. Also found two real, smaller gaps (a candidate generation's `State` isn't always `FAILED` after a `Replicate`-stage failure; `SearchKnowledge` returns an empty result rather than `ErrNoActiveGeneration` for a project resolving an unpromoted version) — recommended, not yet filed as their own tickets.
- [VALID-002](VALID-002-cross-project-version-isolation-fixture.md) — **done.** A standing fixture (two projects, same dependency, two versions) proving `ModeProject` structurally excludes the other project's version — and confirming `ModeCompare`/`ModeAllRetained` correctly do the opposite (merge both versions), exactly as documented.
- [VALID-003](VALID-003-jit-sync-latency-benchmark.md) — **done.** Turned the one anecdotal JIT-sync timing (~6.3s) into a repeatable, recorded live benchmark: `github.com/spf13/pflag` ~5.9s, `github.com/stretchr/testify` ~8.1s (2026-09-17).
- [VALID-004](VALID-004-private-dependency-fixture.md) — **done.** A private/disconnected dependency fixture with a real, source-breaking v1→v2 API change. **Found and fixed a real, previously-undiscovered bug along the way**: `internal/normalize/godoc` never extracted any package-level constructor function (`NewFoo()`/`Open()`/`Connect()` returning a type) — `go/doc` groups those under the returned type's own `Funcs` field, and the normalizer only ever read `Methods`. Every such constructor in every dependency depctl has ever synced was silently unindexed. Fixed, with a regression test — see the ticket's own post-implementation note.
- [POS-001](POS-001-positioning-language-update.md) — **done.** The one doc-language ticket. Its original premise (README/architecture.md leading with generic "documentation RAG" framing) turned out already false when picked up — the real gap was just the missing honest non-exclusivity scoping statement, now added to `README.md`.

## Non-goals

- No cross-system benchmark harness, no Grounded Docs adapter, no Context7-style comparison runner — that's `context-evals` scope per the note above.
- No LLM-judge-based grading of anything — matches epic 26's existing "deterministic, LLM-free" constraint.
