# Epic: External Evaluation Integrations

Not release-blocking for v0.1. Lets users plug `ragctl`'s deterministic eval data and OpenTelemetry traces into existing external evaluation/observability tools (Ragas, DeepEval, Promptfoo, Phoenix/Langfuse) without adding any of them as core dependencies.

## Tickets

- [EXT-001](EXT-001-ragas-export.md) — `eval export --format ragas`.
- [EXT-002](EXT-002-deepeval-export.md) — `eval export --format deepeval`.
- [EXT-003](EXT-003-promptfoo-examples.md) — Example Promptfoo configs for wrong-version, prompt-injection, and stale-docs scenarios.
- [EXT-004](EXT-004-phoenix-langfuse-guide.md) — Guide for pointing OTel export at Phoenix/Langfuse.
