# RES-002: Command execution helper

**Epic:** Resolver Framework
**Status:** done
**Depends on:** RES-001
**Estimated size:** small

## Goal
Build a single, safe subprocess-execution helper (`go list`, `cargo metadata`, `git`, etc. all run through it) with context cancellation, timeouts, and captured output.

## Non-goals
- No shell interpretation/expansion — this runs argv-style commands only, never through `sh -c`.
- No generic "plugin execution" protocol here (that's a much later out-of-process resolver/backend extensibility concern, explicitly deferred).

## Simplicity constraints
- One function, not a builder/options-pattern API with a dozen chained methods — a single `Run(ctx, RunOptions) (RunResult, error)` call is enough.

## Design
Package: `internal/executil`

```go
type RunOptions struct {
    Dir     string
    Args    []string      // Args[0] is the binary name; no shell involved
    Timeout time.Duration // 0 = use context deadline only
    Env     []string      // additional env vars, appended to os.Environ()
}

type RunResult struct {
    Stdout   []byte
    Stderr   []byte
    ExitCode int
}

func Run(ctx context.Context, opts RunOptions) (RunResult, error)
```
Implementation uses `exec.CommandContext` (never `exec.Command` + manual shell string), sets `Dir`, applies `Timeout` via `context.WithTimeout` layered on the passed `ctx`, and captures stdout/stderr to separate buffers (not combined, so JSON-parsing callers like `go list -m -json` aren't polluted by stderr).

A non-zero exit code is *not* automatically an `error` return from `Run` — callers (resolvers) decide whether a given exit code is meaningful (e.g. `go list` can partially succeed with warnings on stderr). `Run` returns a Go `error` only for process-launch failures (binary not found, context cancelled/timed out).

## Inputs / Outputs
- Input: `RunOptions` (binary, args, working dir, timeout).
- Output: `RunResult` with captured stdout/stderr/exit code, or an error if the process could not be started/completed due to cancellation.

## Failure behavior
- Context cancellation/timeout: `Run` returns promptly (process is killed via `CommandContext`'s built-in behavior) with a wrapped `context.DeadlineExceeded`/`context.Canceled`.
- Binary not found: wrapped `exec.ErrNotFound`-based error.

## Tests
- Successful command captures stdout correctly.
- Non-zero exit code is captured in `RunResult.ExitCode` without `Run` itself erroring.
- Timeout: a deliberately slow command (e.g. `sleep 5` with a 100ms timeout) is killed and `Run` returns promptly with a context-deadline error.
- Context cancellation from the caller side kills the subprocess.
- `Dir` is respected (command runs with the correct working directory, verified via `pwd`/`os.Getwd`-equivalent test command).

## Acceptance criteria
- [x] No shell expansion occurs by default (argv passed directly to `exec.CommandContext`).
- [x] Context cancellation and timeout both promptly terminate the subprocess.
- [x] stdout/stderr/exit code are all captured and returned.
- [x] This helper is the one used by every later resolver (GO-002, PY-*, NODE-*, RUST-002, JAVA-*) — no resolver shells out independently.
