# Epic: Competitive Validation (repository-state authority)

Forced by a 2026-09 deep-dive comparison against Grounded Docs (`docs-mcp-server`), an open-source, locally-hosted, MIT-licensed documentation MCP server that turned out to be substantially more mature than initially represented: real job persistence, hybrid lexical/vector search over SQLite+FTS5+sqlite-vec, broad document/package ingestion, and its own retrieval benchmark (MRR/Recall@K/nDCG against Context7).

That comparison converged on one specific, testable claim as ragctl's actual differentiator, not "better RAG in general":

> Grounded Docs manages versioned documentation collections. ragctl manages the relationship between a repository's *resolved dependency state* and the knowledge an agent is permitted to retrieve.

This epic exists to turn that claim into evidence rather than positioning copy — deterministic, in-repo, ragctl-owned tests that prove (or disprove) the specific lifecycle guarantees the rest of the codebase already claims to make (atomic promotion, no silent cross-version fallback, project-reference-aware retention), plus the fixtures a later comparative benchmark would need. It does **not** build a cross-system benchmark harness itself — see the note below.

## Relationship to epic 26 (evaluation framework)

Epic 26's own INDEX already flags an unresolved split: deterministic, ragctl-owned correctness gates (EVAL-002/005) stay in this repo; comparative retrieval-quality benchmarking across systems (EVAL-001/003/004 — Precision@K/Recall@K/MRR/nDCG, a CLI/reporting surface) was proposed to move to a separate `context-evals` repo that grades ragctl only through its public CLI/MCP surface, never importing internal packages.

This competitive review reaffirms that split rather than reopening it: **a ragctl adapter for Grounded Docs' benchmark runner, and the five-scenario ragctl-vs-Grounded-Docs head-to-head test plan, belong in `context-evals` (or Grounded Docs' own benchmark repo, which already supports adding a provider), not here.** `context-evals` doesn't exist yet; nothing in this epic is blocked on it existing, and nothing here should grow into building it. VALID-004's fixture is deliberately written to be reusable by that future harness without ragctl depending on it.

## Tickets

- [VALID-001](VALID-001-atomic-promotion-under-failure.md) — Prove the "never silently serve a stale/mismatched version" invariant under an injected mid-build failure (the interrupted-upgrade scenario from the comparison).
- [VALID-002](VALID-002-cross-project-version-isolation-fixture.md) — A standing fixture (two projects, same dependency, two versions) with an acceptance test that a version-mismatched chunk is structurally unreachable, not just usually absent.
- [VALID-003](VALID-003-jit-sync-latency-benchmark.md) — Turn this session's one anecdotal JIT-sync timing (~6.3s live-verified) into a repeatable, recorded cold-sync latency measurement.
- [VALID-004](VALID-004-private-dependency-fixture.md) — A private/disconnected dependency fixture with a deliberate v1→v2 API break, for both this epic's own tests and any future external comparative benchmark.

## Non-goals

- No cross-system benchmark harness, no Grounded Docs adapter, no Context7-style comparison runner — that's `context-evals` scope per the note above.
- No LLM-judge-based grading of anything — matches epic 26's existing "deterministic, LLM-free" constraint.
- No positioning/marketing copy — see [POS-001](POS-001-positioning-language-update.md) for the one doc-language ticket this comparison also surfaced, kept separate since it has no test/acceptance-criteria shape the other tickets do.
