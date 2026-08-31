# CLI-003: CLI command skeleton

**Epic:** Configuration and CLI Skeleton
**Status:** planned
**Depends on:** CLI-002
**Estimated size:** small

## Goal
Register every top-level `ragctl` command as a Cobra command so the CLI surface is stable, even before most commands are implemented.

## Non-goals
- No actual implementation of `scan`, `deps`, `plan`, `sync`, `status`, `doctor`, `gc`, `watch`, `serve`, `backend`, `registry` — those arrive in their own milestones.

## Simplicity constraints
- Placeholder commands are literally `return fmt.Errorf("feature not implemented in this build")` inside `RunE` — no stub logic, no partial behavior that could be mistaken for a real implementation.

## Design
Package: `cmd/ragctl` (root command wiring) + `internal/cli` (subcommand definitions), using `github.com/spf13/cobra`.

Top-level commands registered on the root:
```
init      (implemented, CLI-002)
scan
project
deps
plan
sync
status
doctor
gc
watch
serve
backend
registry
```
Each unimplemented command's `RunE` returns the exact error string `"feature not implemented in this build"` (exit code 1), rather than doing nothing silently or printing help only.

`config` command (`ragctl config validate`) is also wired here per CLI-001.

## Inputs / Outputs
- Input: CLI args/flags per command (flags can be stubbed too, just enough for `--help` to render sensibly).
- Output: for unimplemented commands, stderr message `feature not implemented in this build` and non-zero exit code.

## Failure behavior
Unimplemented commands must fail loudly and identically — never silently no-op (this is explicitly called out in the plan to prevent confusing "it did nothing" behavior).

## Tests
- For each unimplemented command, run it and assert stderr contains `feature not implemented in this build` and exit code is non-zero.
- `ragctl --help` lists all top-level commands.

## Acceptance criteria
- [ ] All 13 top-level commands are registered and appear in `--help`.
- [ ] Unimplemented commands return the exact "feature not implemented in this build" error rather than silently doing nothing.
- [ ] `init` (CLI-002) and `config validate` (CLI-001) are fully functional, not placeholders.
