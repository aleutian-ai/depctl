# Epic: Status and Doctor

Operational visibility commands: `status` for a quick health/activity snapshot, `doctor` for deeper diagnostics with actionable exit codes. Corresponds to Milestone 17 in the implementation plan.

## Tickets
- [OPS-001](OPS-001-ragctl-status.md) — `ragctl status` (text + `--json`): projects, jobs, storage size, backend health, last sync.
- [OPS-002](OPS-002-ragctl-doctor.md) — `ragctl doctor`: fixed health-check list with severity-based exit codes (0/1/2).
