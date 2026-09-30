# OBS-001: Structured logging

**Epic:** Observability
**Status:** done
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
- [x] `NewLogger` implemented and used by CLI entrypoint and daemon.
- [x] Standard field names used consistently across at least one representative call site per lifecycle stage.
- [x] Unit test verifies JSON output shape.

## Post-implementation notes (2026-09-30)

**Field naming: real standards, not invented ones.** Before writing any code,
researched how this vocabulary is actually consumed downstream (per explicit
request, so logs would be genuinely usable by tools like Phoenix, Arize, or
promptfoo, not just internally consistent). Two live conventions cover
ragctl's own operations exactly:

- [OpenTelemetry GenAI semantic conventions](https://opentelemetry.io/docs/specs/semconv/gen-ai/)
  (now its own `semantic-conventions-genai` spec) — `gen_ai.operation.name`,
  `gen_ai.tool.name`.
- [OpenInference](https://github.com/Arize-ai/openinference) (Arize Phoenix's
  own spec, built on OTel) — `embedding.model_name`, and the
  retrieval-document vocabulary that motivated `retrieval.top_k` /
  `retrieval.documents.count`.

Where ragctl's own operation genuinely is the thing a convention names
(embedding a batch, serving an MCP tool call), the real key is used verbatim
— `embedding.model_name` on embed, `gen_ai.tool.name` on every MCP tool call,
`retrieval.top_k`/`retrieval.documents.count` on query. Everything
ragctl-specific (project id, dependency, version, job id, generation,
backend, stage) is namespaced under `ragctl.*`, the OTel-recommended way to
add custom attributes without risking a future collision with a real
convention key. This also means OBS-002 (OpenTelemetry spans) can reuse
these exact attribute keys as span attributes later with zero renaming.

**Architecture: context-carried logger, zero signature changes.** Every
lifecycle function already takes `context.Context` first, so the logger
rides along via `observability.WithLogger`/`observability.FromContext`
rather than a new `*slog.Logger` parameter threaded through every call —
explicitly allowed by this ticket's own design note ("via context or
explicit parameter"). `FromContext` falls back to `slog.Default()` if no
logger was ever injected, matching the ticket's own failure-behavior
requirement that logging never panic or block correctness — and this exact
fallback is what surfaced the real bug below (wrong-but-present output,
rather than a crash).

**Coverage.** Every stage named in this ticket's Design section logs at
least one completion (or failure) event with `duration_ms`: resolve
(`internal/cli/scan.go`), acquire/normalize (`internal/data/generation/build.go`),
embed/replicate (`internal/data/generation/replicate.go`), validate
(`internal/lifecycle/validate/run.go`), promote
(`internal/lifecycle/promote/promote.go`), gc (`internal/cli/gc.go`), and
query (`internal/query/search.go`). The MCP tool-call boundary
(`internal/mcp/tools.go`) is covered uniformly via a single generic
`withToolLogging[In, Out any]` wrapper applied to all ten tool registrations
in `registerTools`, so a future tool can't be added without logging by
omission.

**Two real bugs found via live verification** (real daemon, real Qdrant
container via podman, real fixture project, real `ragctl serve` subprocess
driven by a real MCP client — not just unit tests):

1. **HTTP handler contexts didn't inherit the daemon's base context.**
   `net/http.Server` gives every incoming request a context rooted in the
   connection's own lifecycle (`r.Context()`), never derived from whatever
   context `Server.Serve(ctx)` was originally called with. Handlers that
   built their working context from `r.Context()` directly (`handleResolve`,
   `handleSearch`, and by extension every other HTTP handler) silently lost
   the logger injected on the daemon's outer context in `runDaemonRun`,
   falling back to `slog.Default()` — visible live as `resolve completed`
   printing in Go's plain-text default format while `acquire`/`embed`/
   `validate`/`promote` correctly printed as JSON (those go through the
   scheduler's own `runCtx`, which *is* derived correctly). Fixed generically
   with a `loggingMiddleware` wrapping the whole `mux` in
   `Server.routes()` (`internal/daemon/server.go`), rather than patching each
   handler individually — covers every current and future handler.
2. **`RunGC`'s early-return paths skipped the completion log entirely.**
   The zero-candidates and dry-run exits returned before reaching the log
   call. Fixed by refactoring `RunGC` to named returns plus a single
   `defer func() { ... }()` that logs exactly once regardless of which exit
   path is taken. Caught during review, then confirmed live: a real
   `ragctl gc` with zero candidates correctly produced
   `{"msg":"gc completed",...,"candidates":0,"deleted":0,"failed":0}`.

**Live verification result.** Driving a real `ragctl serve` subprocess with
a real MCP client (`sdkmcp`) calling `search_dependency_docs` against a real
daemon produced, on the server's stderr:

```json
{"time":"2026-09-30T09:05:55.939352-04:00","level":"INFO","msg":"mcp tool call completed","gen_ai.tool.name":"search_dependency_docs","duration_ms":1078}
```

confirming the full path — MCP tool call in the `ragctl serve` process,
scheduler-routed daemon operations, and now every HTTP-handler-routed
operation — all emit correctly-configured JSON through the same
`internal/observability` package.

(One unresolved diagnostic dead-end along the way, noted for the record:
copying the freshly built binary into a `/tmp` scratch directory made it
exit immediately with `SIGKILL (Code Signature Invalid)` — a macOS
ad-hoc-signature artifact of `cp`-ing a Go binary into `/tmp` under this
OS version, fixed with `codesign --force -s -`, unrelated to this ticket's
code.)
