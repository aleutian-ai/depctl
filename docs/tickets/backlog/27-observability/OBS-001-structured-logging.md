# OBS-001: Structured logging

**Epic:** Observability
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
Adopt `log/slog` project-wide with a consistent set of structured fields so lifecycle events are greppable/parseable from day one.

## Non-goals
- No third-party logging framework.
- No log shipping/aggregation integration.

## Simplicity constraints
- Use the Go standard library `log/slog` only — no `zap`, `zerolog`, etc.
- Provide one small helper (`internal/observability/log.go`) that builds a configured `*slog.Logger` (text handler for terminal, JSON handler when `--json`/non-TTY); do not build a logging abstraction layer on top of slog.

## Design
Package: `internal/observability`

```go
func NewLogger(w io.Writer, json bool, level slog.Level) *slog.Logger
```

Standard field names to use consistently via `slog.Group` or flat keys wherever relevant:

```text
project_id
dependency
version
job_id
generation
backend
duration_ms
```

Every long-running operation (resolve, acquire, normalize, embed, replicate, validate, promote, gc, query) logs at least a start/end pair (or single completion log with `duration_ms`) using these fields. Inject the logger via context or explicit parameter — do not use a global logger singleton beyond a default fallback.

## Inputs / Outputs
- Input: CLI/config flags for log level and format.
- Output: structured log lines to stdout/stderr.

## Failure behavior
Logging itself must never panic or block program correctness; a broken log writer degrades to stderr fallback.

## Tests
- JSON handler produces valid JSON with the standard fields present for a sample operation.
- Log level filtering works (debug suppressed at info level).

## Acceptance criteria
- [ ] `NewLogger` implemented and used by CLI entrypoint and daemon.
- [ ] Standard field names used consistently across at least one representative call site per lifecycle stage.
- [ ] Unit test verifies JSON output shape.
