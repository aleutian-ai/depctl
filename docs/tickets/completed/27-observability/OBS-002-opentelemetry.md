# OBS-002: OpenTelemetry tracing

**Epic:** Observability
**Status:** done
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
- [x] Each listed pipeline stage wraps its work in a span via the helper.
- [x] Tracing is off by default; enabling requires explicit config.
- [x] Exporter failure does not affect operation correctness.

## Post-implementation notes (2026-09-30)

**Package: `internal/observability/trace`.** `StartSpan(ctx, name, attrs...) (context.Context, func())` is the one-liner every call site uses; `RecordError(ctx, err)` is the failure-path counterpart, recording and setting span status without every call site touching the OTel SDK's `codes`/span API directly. `InitProvider(ctx, cfg config.OTelConfig, logger *slog.Logger) (shutdown func(context.Context) error, err error)` is called once, in `runDaemonRun` — the only process that actually executes the lifecycle pipeline. When `cfg.Enabled` is false (default), it returns a no-op shutdown and never touches `otel.SetTracerProvider` at all — `otel.Tracer(...).Start` then resolves to the SDK's own built-in no-op tracer, so `StartSpan`/`RecordError` are always safe to call from every stage unconditionally, with zero export path compiled into the hot path (this ticket's own simplicity constraint).

**Config:** `observability.otel.{enabled,endpoint}` (`internal/config.ObservabilityConfig`/`OTelConfig`), validated so `enabled: true` requires a non-empty `endpoint`.

**Endpoint format — a real bug found and fixed via live verification.** The first implementation used `otlptracehttp.WithEndpointURL(cfg.Endpoint)`, which — unlike `WithEndpoint` — takes the given URL as the literal request target and never appends OTLP's standard `/v1/traces` path. Driving this against a real Jaeger container produced a real, consistent `404 Not Found` on every export, silently (per this ticket's own failure-behavior requirement — the daemon itself never noticed, no spans ever arrived). Fixed with a small `exporterOptions` helper: it accepts either a plain `host:port` or a full `http(s)://host:port` URL in config, uses `otlptracehttp.WithEndpoint` (which always appends `/v1/traces`, the same behavior every other OTel SDK gives `OTEL_EXPORTER_OTLP_ENDPOINT`), and infers `WithInsecure()` from an `http://` scheme or a bare host:port.

**Coverage.** Every span name from this ticket's Design section is emitted by a real call site: `resolve` (`internal/cli/scan.go`), `plan` (`internal/cli/plan.go`), `acquire`/`normalize`/`chunk` (`internal/data/generation/build.go` — `chunk` wraps `indexObjects`, the function that fingerprints, chunks, and stores objects), `embed`/`replicate` (`internal/data/generation/replicate.go` — one `embed` span per batch, one `replicate` span for the whole call), `validate` (`internal/lifecycle/validate/run.go`), `promote` (`internal/lifecycle/promote/promote.go`), `query` (`internal/query/search.go`), `gc` (`internal/cli/gc.go`). The MCP tool-call boundary (`internal/mcp/tools.go`'s `withToolLogging`) additionally emits an `mcp.tool_call` span tagged with the same `gen_ai.tool.name` attribute OBS-001's log line already carries — attribute keys never diverge between the log and the span for the same event, by construction (both wrappers read from the same `observability.Key*` constants).

**Live verification.** Real Jaeger (`jaegertracing/all-in-one`) and a fresh isolated Qdrant, both via podman, plus the real production Ollama (read-only — embedding calls don't mutate it). Ran a real `ragctl scan`+`sync`+`sync --rebuild`+`gc --dry-run` against a real fixture (a tiny Go module depending on `github.com/google/uuid`), then a real MCP `search_dependency_docs` call through a real `ragctl serve` subprocess. Querying Jaeger's own API afterward (`/api/services`, `/api/traces?service=ragctl`) confirmed a real `ragctl` service with all eleven span names present across real trace IDs — not simulated, not just unit-tested. Also re-ran `hack/test-linux.sh` (Alpine/Podman) after these changes; all packages pass there too.

**New dependencies.** `go.opentelemetry.io/otel/sdk`, `.../trace`, `.../exporters/otlp/otlptrace/otlptracehttp`, `.../semconv/v1.26.0` — `otel`/`otel/trace` were already indirect dependencies (via the MCP SDK's `otelhttp` instrumentation); this ticket promotes them to direct and adds the SDK/exporter packages. `go mod tidy` bumped several transitive versions (grpc, golang.org/x/* family) as a side effect — verified with a full `go build`/`go vet`/`go test ./...` (native and Linux) afterward, no regressions.
