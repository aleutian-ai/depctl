# Epic: Retention and GC

Keeps the local knowledge corpus bounded: retain the versions a project (or explicit policy) actually needs, hold recently-dropped versions for a grace period, and garbage-collect everything else in a restartable, idempotent way. Corresponds to Milestone 15 in the implementation plan.

## Tickets
- [RET-001](RET-001-reference-accounting.md) — Track which projects/reasons reference each dependency version (bbolt `references` bucket).
- [RET-002](RET-002-grace-period.md) — Auto-add a time-bound `grace_period` reference when a version's last project reference is dropped.
- [RET-003](RET-003-gc-planner.md) — Pure planner computing GC-eligible versions from reference/grace/pin/active state.
- [RET-004](RET-004-depctl-gc.md) — `depctl gc` command: execute the plan, deleting vector/Badger/bbolt data in order, restartably.
