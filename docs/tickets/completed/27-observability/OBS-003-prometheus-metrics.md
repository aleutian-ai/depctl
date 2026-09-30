# OBS-003: Prometheus metrics endpoint

**Epic:** Observability
**Status:** done
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
- [x] All nine metrics implemented with correct types (counter/gauge/histogram).
- [x] Endpoint binds to localhost by default, only when enabled.
- [x] Test scrapes the endpoint and asserts metric presence.

## Post-implementation notes (2026-09-30)

**Package: `internal/observability/metrics`.** All nine metrics are package-level vars registered once via `promauto` against `client_golang`'s default registry (this ticket's own simplicity constraint — no custom metrics abstraction). `StartServer(cfg config.MetricsConfig, logger *slog.Logger) (shutdown func(context.Context) error, err error)` binds `/metrics` only when `cfg.Enabled`; per this ticket's own failure-behavior requirement, a bind failure is returned as a real error (fatal) only because metrics were explicitly enabled — disabled is a pure no-op, no listener ever created. `config.MetricsConfig.ListenOrDefault()` defaults to `127.0.0.1:9090`, never `0.0.0.0`, matching the project's local-first security defaults.

**Call sites reuse OBS-001/OBS-002's own instrumentation points**, per this ticket's own design note ("wire at the same call sites"): `AcquireSeconds`/`NormalizeSeconds` in `internal/data/generation/build.go`, `EmbedSeconds`/`BackendUpsertSeconds` in `internal/data/generation/replicate.go` (one observation per batch), `SyncJobsTotal{type,state}`/`SyncFailuresTotal` in `internal/cli/sync.go`'s `runSyncAction` (a `defer`, so every action-kind exit path — success, failure, skip — records exactly once, the same pattern `RunGC`'s own defer-based logging already established), and `GCCandidates` in `internal/cli/gc.go`'s `RunGC` right after planning.

**`ActiveGenerations`/`BadgerBytes` needed a different treatment — a real gap found during live verification, not assumed.** These two have no natural per-request call site (nothing on ragctl's own request path needs "how many active generations exist right now"); the first implementation set them only inside `buildStatus` (the function backing `ragctl status`/`/v1/status`), which meant a real Prometheus scrape between `status` calls would read stale or zero-value data — confirmed live: right after a fresh daemon restart, `/metrics` reported `ragctl_active_generations 0` despite a real active generation existing, until a manual `ragctl status` call refreshed it. Fixed with `refreshStorageMetrics` (`internal/cli/status.go`), a small periodic goroutine — matching the existing `checkEmbeddingReadiness`/`checkVectorReadiness` background-goroutine pattern already established in `internal/cli/daemon.go` — started only when metrics are enabled, computing both gauges once immediately and then every 30s.

**Live verification.** A real Prometheus container (`prom/prometheus`) and a fresh isolated Qdrant, both via podman, scraping a real isolated `ragctl daemon run` (real Ollama, read-only, for embedding) over `host.containers.internal`. Ran a real `scan`+`sync`+`sync --rebuild`+`gc --dry-run` against a real fixture, then queried Prometheus's own HTTP API (`/api/v1/targets`, `/api/v1/query`) directly — confirmed `health: "up"` with no scrape error, and real, non-empty query results for `ragctl_active_generations` (immediately correct on daemon startup, before any `ragctl status` call, once the periodic-refresh fix landed), `ragctl_sync_jobs_total{type="SYNC_VERSION",state="success"}`, and `ragctl_embed_seconds_count`. Also re-ran `hack/test-linux.sh` (Alpine/Podman) afterward — all packages pass there too.

**New dependency:** `github.com/prometheus/client_golang` (direct), pulling in `prometheus/client_model`, `prometheus/common`, `prometheus/procfs`, `beorn7/perks` as transitive deps — verified with a full `go build`/`go vet`/`go test ./...` (native and Linux) afterward, no regressions.
