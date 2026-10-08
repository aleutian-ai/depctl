# Epic: Planner and Sync

Computes the gap between what projects actually depend on and what knowledge is currently active, then executes it. The planner (PLAN-001) is a pure diff function over already-resolved state; `depctl plan` (PLAN-002) surfaces that diff for human review; `depctl sync` (PLAN-003) executes it by driving the generation-builder, embedding, vector-replication, validation, and promotion pipelines built in earlier epics. This is the last piece of the v0.1 critical path before MCP exposes the result to agents.

## Tickets
- [PLAN-001](PLAN-001-desired-state-model.md) — Pure desired-state diffing function producing typed actions (ADD/DROP reference, SYNC version, RETAIN, GC candidate, NOOP).
- [PLAN-002](PLAN-002-depctl-plan-command.md) — `depctl plan` CLI command: read-only report of computed actions, human-readable or `--json`.
- [PLAN-003](PLAN-003-depctl-sync-command.md) — `depctl sync` CLI command: executes the plan end-to-end, with `--dry-run`, `--project`, `--dependency`, `--offline`, `--force` flags.
