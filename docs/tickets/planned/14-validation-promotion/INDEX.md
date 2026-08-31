# Epic: Validation and Promotion

This is the first major proof point of the whole system: a candidate generation must pass deterministic structural and correctness checks before it can become visible to agents, and the switchover to "active" must be atomic so retrieval never sees a half-ingested or version-mixed state. Everything here is deterministic and testable without an LLM, per the project's core development principle.

## Tickets
- [VAL-001](VAL-001-structural-validator.md) — Structural checks: non-zero counts, backend point count match, manifest completeness, valid version metadata.
- [VAL-002](VAL-002-sanity-thresholds.md) — Guardrails comparing candidate to prior generation to catch implausible count collapses/spikes.
- [VAL-003](VAL-003-version-correctness-smoke-test.md) — Deterministic smoke test proving indexed content returns only its own version's metadata.
- [VAL-004](VAL-004-atomic-promotion.md) — Single bbolt write transaction that atomically promotes a validated candidate and supersedes the prior active generation.
