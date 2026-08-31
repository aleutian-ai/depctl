# RET-002: Grace period

**Epic:** Retention and GC
**Status:** planned
**Depends on:** RET-001
**Estimated size:** small

## Goal
When a dependency version's last project reference is removed, keep its knowledge retained for a configurable grace period instead of deleting it immediately.

## Non-goals
- GC eligibility logic and deletion (RET-003, RET-004).

## Simplicity constraints
- One config field, one derived reference reason (`grace_period`). Do not build a general-purpose scheduling/timer subsystem — grace expiry is just a timestamp comparison evaluated lazily by RET-003 at GC-plan time.

## Design
Config addition (`internal/config`):
```yaml
retention:
  grace_period: 336h # default 14 days
```

When `RemoveReference` (RET-001) drops a project's reference and `CountReferences` for that version reaches zero (excluding `latest`/`manual_pin` reasons), automatically call `AddReference` with:
```go
VersionReference{
    ProjectID: "_grace",
    Reason:    "grace_period",
    LastSeenAt: time.Now(), // grace expiry = LastSeenAt + config.Retention.GracePeriod
}
```
This logic lives in the planner/reference-accounting call site (wherever `RemoveReference` is invoked), not inside the bbolt store itself.

## Inputs / Outputs
- Input: reference-count-reaches-zero event, configured grace duration.
- Output: a `grace_period` reference record with an expiry-computable timestamp.

## Failure behavior
- If grace period config is missing, default to 14 days (336h); never treat missing config as "no grace period" (that would silently GC on next cycle).

## Tests
- Removing the last project reference for a version creates a `grace_period` reference.
- `grace_period` reference's effective expiry = `LastSeenAt + grace_period` is computed correctly at various durations (0h, 336h, custom).
- A version with an active `latest` or `manual_pin` reference does not get a `grace_period` reference added when its project reference drops.

## Acceptance criteria
- [ ] Default grace period is 14 days.
- [ ] Grace period is configurable via `retention.grace_period`.
- [ ] Last project reference disappearing adds a `grace_period` reference automatically.
