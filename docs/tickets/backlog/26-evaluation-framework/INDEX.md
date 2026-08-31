# Epic: Evaluation Framework

Proves that `ragctl` improves knowledge correctness with deterministic, LLM-free checks before anything else (LLM-judge based evaluation is explicitly out of scope for v1). This epic gives the project a way to measure "did version-correct retrieval actually work" and, optionally, to block promotion of a generation that regresses it.

## Tickets

- [EVAL-001](EVAL-001-eval-case-model.md) — Define the `EvalCase` struct and a YAML fixture loader.
- [EVAL-002](EVAL-002-deterministic-version-correctness.md) — Compute Version Correctness / Stale Retrieval / Provenance Completeness / Authority Compliance against the query service.
- [EVAL-003](EVAL-003-retrieval-metrics.md) — Compute Precision@K, Recall@K, MRR, nDCG for labeled benchmark cases (informational only).
- [EVAL-004](EVAL-004-ragctl-eval-cli.md) — `ragctl eval run|list|show|export` CLI commands.
- [EVAL-005](EVAL-005-promotion-gate-integration.md) — Optionally gate generation promotion on EVAL-002 thresholds.
