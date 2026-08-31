# RET-003: GC planner

**Epic:** Retention and GC
**Status:** planned
**Depends on:** RET-002
**Estimated size:** small

## Goal
Compute the set of dependency versions eligible for garbage collection, applying all retention rules deterministically.

## Non-goals
- Actually deleting anything (RET-004).

## Simplicity constraints
- Pure function over store reads: `PlanGC(ctx) ([]GCCandidate, error)`. No side effects, no persistence of "candidate" state beyond what RET-001/002 already store.
- Do not add a separate "GC policy plugin" interface; one hard-coded eligibility rule is enough for v0.1.

## Design
Package: `internal/retention`.

```go
type GCCandidate struct {
    Ecosystem Ecosystem
    Package   string
    Version   string
    Reason    string // e.g. "grace_expired"
}

func PlanGC(ctx context.Context, store ControlStore, now time.Time) ([]GCCandidate, error)
```

Eligibility (a version is GC-eligible only if ALL hold):
```text
no project references (reason == "project", count == 0)
AND not latest (no reason == "latest" reference)
AND not manually pinned (no reason == "manual_pin" reference)
AND grace_period reference exists and now > LastSeenAt + config.Retention.GracePeriod
AND version is not the ACTIVE generation for that dependency
```

Iterate the `dependency_versions` bucket (from STORE-001), call `ListReferences` per version, apply the rule.

## Inputs / Outputs
- Input: current time, full reference/version state from bbolt.
- Output: ordered list of `GCCandidate`.

## Failure behavior
- A store read error aborts planning and returns a wrapped error; never returns a partial/ambiguous candidate list.

## Tests
- Version with zero references and expired grace period → eligible.
- Version with a `manual_pin` reference → never eligible regardless of grace expiry.
- Version that is the current ACTIVE generation → never eligible even with zero project references.
- Version with grace reference not yet expired → not eligible.

## Acceptance criteria
- [ ] Eligibility requires all four conditions (no project refs, not latest, not pinned, grace expired) plus not-active.
- [ ] Deterministic given the same store state and `now`.
