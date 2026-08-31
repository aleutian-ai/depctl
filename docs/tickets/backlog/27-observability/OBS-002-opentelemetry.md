# OBS-002: OpenTelemetry tracing

**Epic:** Observability
**Status:** planned
**Depends on:** OBS-001
**Estimated size:** medium

## Goal
Instrument the lifecycle pipeline with OpenTelemetry spans, off by default, so operators can opt into tracing for debugging without imposing overhead on the default local-only install.

## Non-goals
- No always-on tracing.
- No bundled tracing backend (Jaeger/Tempo) — export configuration only, pointing at whatever the user runs.

## Simplicity constraints
- Wrap span creation in one thin helper package so call sites stay a one-liner; do not scatter raw OTel SDK calls throughout business logic.
- OTel dependency and exporter wiring must be fully inert (no background exporter goroutines, no network calls) when tracing is disabled in config.

## Design
Package: `internal/observability/trace`

```go
func StartSpan(ctx context.Context, name string) (context.Context, func())
```

Span names, one per pipeline stage:

```text
resolve
plan
acquire
normalize
chunk
embed
replicate
validate
promote
query
gc
```

Config:

```yaml
observability:
  otel:
    enabled: false
    endpoint: ""
```

When `enabled: false` (default), `StartSpan` uses a no-op tracer provider (OTel SDK's built-in noop), so there is zero export path compiled into the hot path logic — just a config check at tracer-provider construction time in `main`.

## Inputs / Outputs
- Input: config, context.
- Output: spans exported to configured OTLP endpoint when enabled; no-ops otherwise.

## Failure behavior
Exporter connection failure must never fail the operation being traced — log a warning once, keep operating.

## Tests
- With tracing disabled, `StartSpan`/`func()` pair is safe to call and adds no measurable overhead (smoke test only, not a benchmark gate).
- With a test OTLP collector, spans for a sample operation appear with correct names/attributes.

## Acceptance criteria
- [ ] Each listed pipeline stage wraps its work in a span via the helper.
- [ ] Tracing is off by default; enabling requires explicit config.
- [ ] Exporter failure does not affect operation correctness.
