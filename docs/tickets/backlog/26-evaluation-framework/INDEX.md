# Epic: Evaluation Framework

Proves that `ragctl` improves knowledge correctness with deterministic, LLM-free checks before anything else (LLM-judge based evaluation is explicitly out of scope for v1). This epic gives the project a way to measure "did version-correct retrieval actually work" and, optionally, to block promotion of a generation that regresses it.

## Status note — conflicts with the `context-evals` proposal (needs a decision, not yet resolved)

`docs/scratch/ragctl_architecture_eval_next_steps-2.md` (§4.3, §12, §15) proposes an *independently useful* eval system living in a **separate repo** (`context-evals`) that grades ragctl, model-only baselines, and other context systems side by side, calling ragctl only through its public CLI/MCP surface — explicitly **never importing ragctl's internal Go packages**, so the benchmark can't accidentally grade implementation details a real agent could never see. That directly conflicts with this epic's design: EVAL-001/003/004 build `internal/eval` as an in-repo package and a `ragctl eval` CLI.

The scratch doc's own split (§12.1 vs §12.2) actually maps cleanly onto splitting this epic rather than deleting it:
- **Stays in ragctl** (deterministic, ragctl-owned correctness gates — same spirit as the existing VAL-001/002/003 promotion checks): EVAL-002's version-correctness/stale-retrieval/provenance/authority metrics, and EVAL-005's promotion-gate wiring. These check *ragctl's own* invariants and are cheap, LLM-free, and already close to what `lifecycle/validate` does.
- **Moves to a separate `context-evals` repo, not built here**: EVAL-001's general case model, EVAL-003's Precision@K/Recall@K/MRR/nDCG (comparative benchmarking across systems, not a ragctl invariant), and EVAL-004's CLI/reporting surface — these are exactly the "independently useful measurement boundary" the scratch doc argues for.

Nobody has decided this yet — the tickets below are left as originally specced. Before picking any of them up, confirm with the user whether to (a) keep this epic in-repo as-is, (b) narrow it to just EVAL-002/005 and spin the rest into a new sibling repo, or (c) drop it entirely in favor of `context-evals` handling all of it externally via MCP/CLI.

## Tickets

- [EVAL-001](EVAL-001-eval-case-model.md) — Define the `EvalCase` struct and a YAML fixture loader.
- [EVAL-002](EVAL-002-deterministic-version-correctness.md) — Compute Version Correctness / Stale Retrieval / Provenance Completeness / Authority Compliance against the query service.
- [EVAL-003](EVAL-003-retrieval-metrics.md) — Compute Precision@K, Recall@K, MRR, nDCG for labeled benchmark cases (informational only).
- [EVAL-004](EVAL-004-ragctl-eval-cli.md) — `ragctl eval run|list|show|export` CLI commands.
- [EVAL-005](EVAL-005-promotion-gate-integration.md) — Optionally gate generation promotion on EVAL-002 thresholds.
