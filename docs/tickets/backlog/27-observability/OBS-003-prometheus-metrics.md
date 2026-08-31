# OBS-003: Prometheus metrics endpoint

**Epic:** Observability
**Status:** planned
**Depends on:** OBS-001
**Estimated size:** small

## Goal
Expose an optional `/metrics` HTTP endpoint with counters/gauges for sync activity, reuse efficiency, and correctness, for operators who want to scrape into Prometheus.

## Non-goals
- No bundled Grafana dashboards (can be an example/doc later, not this ticket).
- No metrics beyond the listed set — do not instrument everything speculatively.

## Simplicity constraints
- Use `github.com/prometheus/client_golang` directly with the default registry; do not build a custom metrics abstraction.
- The endpoint binds only when explicitly enabled in config, on localhost by default (consistent with the project's local-first security defaults).

## Design
Package: `internal/observability/metrics`

Metrics (names from the design spec):

```text
ragctl_sync_jobs_total (counter, labels: type, state)
ragctl_sync_failures_total (counter)
ragctl_acquire_seconds (histogram)
ragctl_normalize_seconds (histogram)
ragctl_embed_seconds (histogram)
ragctl_backend_upsert_seconds (histogram)
ragctl_active_generations (gauge)
ragctl_gc_candidates (gauge)
ragctl_badger_bytes (gauge)
```

Config:

```yaml
observability:
  metrics:
    enabled: false
    listen: 127.0.0.1:9090
```

Wire counters/histograms at the same call sites used for OBS-001 logging and OBS-002 spans, to avoid triplicated instrumentation logic — a single "record operation" helper can update log, span, and metric together per stage.

## Inputs / Outputs
- Input: config; internal lifecycle events.
- Output: Prometheus text-format metrics on the configured endpoint.

## Failure behavior
Endpoint bind failure at startup is a fatal error only if metrics are explicitly enabled; otherwise the daemon runs without it.

## Tests
- `/metrics` returns valid Prometheus text format containing at least one sample per metric after a simulated sync job.
- Endpoint is not bound when `enabled: false`.

## Acceptance criteria
- [ ] All nine metrics implemented with correct types (counter/gauge/histogram).
- [ ] Endpoint binds to localhost by default, only when enabled.
- [ ] Test scrapes the endpoint and asserts metric presence.
