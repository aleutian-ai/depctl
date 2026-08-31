# Epic: Observability

Gives operators visibility into the daemon without imposing overhead by default. Structured logging is always on; tracing and metrics are both opt-in, consistent with the project's "local-first, no telemetry by default" posture.

## Tickets

- [OBS-001](OBS-001-structured-logging.md) — Adopt `log/slog` with standard structured fields across the pipeline.
- [OBS-002](OBS-002-opentelemetry.md) — Off-by-default OpenTelemetry spans across lifecycle stages.
- [OBS-003](OBS-003-prometheus-metrics.md) — Off-by-default `/metrics` endpoint with sync/reuse/correctness counters and gauges.
