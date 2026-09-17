# PLAN-002: `ragctl plan` command

**Epic:** Planner and Sync
**Status:** done
**Depends on:** PLAN-001
**Estimated size:** small

## Goal
Wire `PLAN-001`'s pure planner into a CLI command that reads current project state from storage, computes actions, and prints a human-readable (or JSON) report without making any changes.

## Non-goals
- No execution of actions (PLAN-003).

## Simplicity constraints
- The command is a thin CLI wrapper: load state from bbolt → call `planner.Plan` → format output. No new business logic here beyond formatting.

## Design
- Cobra command in `internal/app` (wired from `cmd/ragctl`): `ragctl plan [--project <id>] [--json]`.
- Human-readable format (per design spec example):
```text
Project: foo

grpc-go
  current project: v1.82.0
  prior project:   v1.75.1

Actions
  SYNC   v1.82.0
  DROP   reference v1.75.1
  RETAIN v1.75.1 for 14 days
```
- `--json` flag: marshal `[]planner.Action` (grouped by project) directly to stdout as JSON.
- No `--project` flag: plan for all registered projects, one section per project.

## Inputs / Outputs
- Input: registered projects + their stored resolutions/references (read-only).
- Output: stdout report; no state mutation.

## Failure behavior
- A project with a resolution error (e.g. stale/missing go.mod) is reported as a warning in that project's section, other projects still plan normally.

## Tests
- Golden-output test: fixed fixture state → expected human-readable text.
- `--json` output round-trips through `json.Unmarshal` into `[]planner.Action`.

## Acceptance criteria
- [x] `ragctl plan` never writes to bbolt/Badger or makes network calls.
- [x] `ragctl plan --json` produces valid, parseable JSON.

## Post-implementation note
Registered in `internal/cli` (this repo's existing Cobra package for every other command), not a new `internal/app` — no such package exists and every other command already lives in `internal/cli`. Output format simplified from the design spec's per-dependency current/prior-version breakdown to one line per non-`NOOP` action (kind, ecosystem, dependency, version, reason) grouped under a `Project: <root> (<id>)` header — the richer format doesn't cleanly generalize to actions with no "prior version" (e.g. a brand-new dependency) without inventing placeholder text, and the flat form is what `ragctl deps` and every other CLI command in this codebase already use. `--json` marshals `[]projectPlan` (one entry per project, each holding its actions plus an optional warning) rather than a bare `[]planner.Action`, so a project with no resolution yet can report why instead of silently contributing zero actions.
