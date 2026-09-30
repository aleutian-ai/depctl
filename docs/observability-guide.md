# Observability guide: logs, traces, and metrics

`ragctl` ships three independent observability layers (epic 27, `OBS-001..003`), consistent with the project's local-first, no-telemetry-by-default posture: structured logging is always on and stays local (stdout/stderr) unless you pipe it somewhere; tracing and metrics are both off until you explicitly enable them in `config.yaml`, and neither one ever dials out on its own otherwise. All three reuse the same field/attribute vocabulary end to end — a log field, a span attribute, and (where it applies) a metric label are never renamed between layers.

Everything below was run for real against a real daemon, a real Qdrant + Ollama, and real Jaeger/Prometheus containers (via `podman`) during this feature's own live verification — the example output is captured from those runs, not hand-written.

Related package docs: [internal/config](internal/config.md). `internal/observability` has no dedicated package doc yet — see the source directly: `internal/observability/log.go`, `internal/observability/trace/trace.go`, `internal/observability/metrics/metrics.go`.

## 1. Structured logging (`OBS-001`) — always on

Every `ragctl` process (the daemon, `ragctl serve`, and every one-shot CLI command) logs through one `*slog.Logger`, injected via `context.Context` rather than threaded as a parameter — see `internal/observability/log.go`. By default this is human-readable text to stderr; JSON is opt-in for log aggregation:

```yaml
log:
  json: true      # false (default): human-readable text
  level: info      # debug | info | warn | error (default: info)
```

A real completion log, captured from a live `ragctl gc` run with `log.json: true`:

```json
{"time":"2026-09-30T08:58:10.10-04:00","level":"INFO","msg":"gc completed","ragctl.stage":"gc","ragctl.backend":"qdrant","duration_ms":3,"dry_run":false,"candidates":0,"deleted":0,"failed":0}
```

And from a real MCP tool call, driven through a real `ragctl serve` subprocess and a real MCP client:

```json
{"time":"2026-09-30T09:05:55.939352-04:00","level":"INFO","msg":"mcp tool call completed","gen_ai.tool.name":"search_dependency_docs","duration_ms":1078}
```

### Field naming — real conventions, not invented ones

Fields were chosen by checking what Phoenix/Arize, promptfoo, and OTel-based tooling actually expect, not by guessing:

| Field | Convention | Used on |
|---|---|---|
| `gen_ai.tool.name` | [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/) | every MCP tool call |
| `gen_ai.operation.name` | OTel GenAI | embed (`"embeddings"`) |
| `embedding.model_name` | [OpenInference](https://github.com/Arize-ai/openinference) (Arize Phoenix's own spec) | embed |
| `retrieval.top_k` / `retrieval.documents.count` | OpenInference | query |
| `ragctl.project_id` / `.dependency` / `.version` / `.job_id` / `.generation` / `.backend` / `.stage` | ragctl-specific, namespaced | every lifecycle stage |
| `duration_ms` | plain, no namespace needed | every completion log |

The `ragctl.*` prefix is the standard OTel-recommended way to add custom attributes without risking a future collision with a real convention key — this is also why OBS-002's spans below reuse these exact same key names as attributes, with zero renaming.

Every stage logs at least one completion (or failure) event: `resolve`, `acquire`, `normalize`, `embed`, `replicate`, `validate`, `promote`, `query`, `gc` — plus the MCP tool-call boundary. See `docs/tickets/completed/27-observability/OBS-001-structured-logging.md` for the full call-site list.

## 2. OpenTelemetry tracing (`OBS-002`) — opt-in

Off by default: `otel.Tracer(...).Start` resolves to the OTel SDK's own built-in no-op tracer until you enable it, so `internal/observability/trace.StartSpan` is always safe to call unconditionally with zero export path compiled into the hot path.

```yaml
observability:
  otel:
    enabled: true
    endpoint: 127.0.0.1:4318   # host:port, or a full http(s):// URL — see note below
```

`endpoint` accepts either a plain `host:port` or a full URL; either way, ragctl's exporter always appends OTLP's standard `/v1/traces` path itself (the same behavior every other OTel SDK gives `OTEL_EXPORTER_OTLP_ENDPOINT`) — you never need to add `/v1/traces` yourself.

### Try it against a real Jaeger

```bash
podman run -d --name ragctl-jaeger -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one:latest
```

Set `observability.otel.enabled: true` and `endpoint: 127.0.0.1:4318` in your `config.yaml`, then run a normal `ragctl scan`/`sync`/`gc`/query through the daemon. Jaeger's own UI is at <http://127.0.0.1:16686>; its API confirms the same thing:

```bash
curl -s http://127.0.0.1:16686/api/services
# {"data":["jaeger-all-in-one","ragctl"],"total":2,...}
```

A real trace from this session's own verification carried every one of these spans, one per pipeline stage, plus the MCP tool-call boundary:

```
resolve · plan · acquire · normalize · chunk · embed · replicate · validate · promote · query · gc
mcp.tool_call (attribute gen_ai.tool.name=<tool name>)
```

A stage's own failure sets the span's error status and records the error (`trace.RecordError`) — visible in Jaeger as a red, errored span — without ever affecting the operation's own return value; a broken/unreachable collector degrades to one warning log line, never a failed sync.

## 3. Prometheus metrics (`OBS-003`) — opt-in

Also off by default; `/metrics` is never bound unless explicitly enabled, and binds to `127.0.0.1` only unless you override `listen` (never `0.0.0.0` by default, matching the project's local-first security posture):

```yaml
observability:
  metrics:
    enabled: true
    listen: 127.0.0.1:9090   # default if omitted
```

### The nine metrics

| Metric | Type | What it tracks |
|---|---|---|
| `ragctl_sync_jobs_total{type,state}` | counter | sync actions processed, by action kind and outcome |
| `ragctl_sync_failures_total` | counter | sync actions that failed, across all types |
| `ragctl_acquire_seconds` | histogram | time spent acquiring a generation's source content |
| `ragctl_normalize_seconds` | histogram | time spent normalizing acquired content |
| `ragctl_embed_seconds` | histogram | time spent embedding one batch of chunks |
| `ragctl_backend_upsert_seconds` | histogram | time spent upserting one batch into the vector backend |
| `ragctl_active_generations` | gauge | active generations for the configured backend, refreshed every 30s |
| `ragctl_gc_candidates` | gauge | GC candidates found by the most recent planning pass |
| `ragctl_badger_bytes` | gauge | on-disk Badger data-store size, refreshed every 30s |

### Try it against a real Prometheus

```bash
cat > prometheus.yml <<'EOF'
scrape_configs:
  - job_name: ragctl
    static_configs:
      - targets: ["host.containers.internal:9090"]
EOF
podman run -d --name ragctl-prom -p 9091:9090 \
  -v "$PWD/prometheus.yml:/etc/prometheus/prometheus.yml:ro" \
  prom/prometheus:latest
```

(`host.containers.internal` is how a podman container reaches the host's own loopback — same convention `hack/test-linux.sh` already uses.) After running a real `ragctl sync`, a real query from Prometheus's own API:

```bash
curl -s 'http://127.0.0.1:9091/api/v1/query?query=ragctl_sync_jobs_total'
```

```json
{"status":"success","data":{"resultType":"vector","result":[
  {"metric":{"__name__":"ragctl_sync_jobs_total","state":"success","type":"SYNC_VERSION"},"value":[...,"1"]},
  {"metric":{"__name__":"ragctl_sync_jobs_total","state":"success","type":"ADD_REFERENCE"},"value":[...,"1"]}
]}}
```

`ragctl_active_generations`/`ragctl_badger_bytes` are correct immediately on daemon startup — a background refresh (every 30s) keeps them current even if no client ever runs `ragctl status`; a bind failure is only fatal when `enabled: true` (nothing to fail when metrics are off).

## Putting it together

All three layers can be on at once with no interaction between them — logging always runs, tracing and metrics are each independently gated by their own `enabled` flag. A minimal "I want everything" config:

```yaml
log:
  json: true
  level: info
observability:
  otel:
    enabled: true
    endpoint: 127.0.0.1:4318
  metrics:
    enabled: true
    listen: 127.0.0.1:9090
```

See also [security-hardening.md](security-hardening.md) for `SEC-005`, the companion invariant that ragctl's *own* telemetry never leaves the machine regardless of any of the above — these three layers are for *you* to point at infrastructure *you* configured; there is no default endpoint ragctl reports to on its own.
