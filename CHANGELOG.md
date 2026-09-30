# Changelog

## v0.1.0 — 2026-09-30

First public release.

### Core pipeline
- Dependency-aware knowledge sync for Go, Python, and Node projects: scan → resolve → sync → serve, with a version-scoped retrieval index kept current as dependencies change.
- MCP server exposing version-correct dependency docs to coding agents, with provenance (trust class, authority, breadcrumb) attached to every result.
- Garbage collection with reference counting and grace periods; orphan-generation cleanup for failed/stuck builds.
- Local-first daemon architecture (single-owner daemon over a Unix socket, auto-start, `ragctl watch` for live re-sync on dependency changes).

### Security hardening
- Every piece of retrieved content carries a trust class (official/repository/community/user/unknown), enforced at validation time.
- Every knowledge-returning MCP tool response is labeled against prompt injection.
- Configurable fetch limits (file size, redirects, mirror size) enforced on every external fetch.
- No downloaded dependency content is ever executed as code — enforced and CI-checked.
- No telemetry leaves the machine — enforced by three CI-run tests, not just documented.

See `docs/security-hardening.md` for a full walkthrough with examples.

### Observability
- Structured logging (`log/slog`) on by default, with field names drawn from real OpenTelemetry GenAI / OpenInference conventions where applicable.
- Optional OpenTelemetry tracing (off by default) across every pipeline stage.
- Optional Prometheus metrics endpoint (off by default): sync outcomes, stage timings, active generations, GC candidates, storage size.

See `docs/observability-guide.md` for a full walkthrough with examples.

### Reliability
- Real-scale, real-concurrency, and adversarial (`kill -9`) stress testing across the full scan → sync → gc → serve loop.
- `ragctl daemon stop` accurately reports whether the daemon process has actually exited, not just whether its socket closed.
- Subprocess calls no longer inherit a stale/deleted working directory from a long-lived daemon process.

See `docs/architecture.md` for the full implementation history and `docs/tickets/completed/` for every shipped ticket.
