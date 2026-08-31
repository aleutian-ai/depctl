# RET-001: Reference accounting

**Epic:** Retention and GC
**Status:** planned
**Depends on:** PLAN-001, STORE-001
**Estimated size:** small

## Goal
Track, per dependency version, which projects (and other reasons) currently require it, so retention/GC decisions can be made from reference counts rather than guesswork.

## Non-goals
- Grace-period timers (RET-002).
- GC eligibility computation (RET-003) and execution (RET-004).

## Simplicity constraints
- One reference record type, one bbolt bucket. Do not build a generic "tagging" or "labeling" subsystem.
- No reference-count caching layer; counting is a bucket prefix scan over a small keyspace.

## Design
Package: `internal/control/bbolt` (extends STORE-001).

```go
type VersionReference struct {
    ProjectID   string
    Ecosystem   Ecosystem
    Package     string
    Version     string
    Reason      string // "project" | "latest" | "manual_pin" | "grace_period"
    FirstSeenAt time.Time
    LastSeenAt  time.Time
}
```

Bucket: `references`, key pattern `references/<ecosystem>/<package>/<version>/<project-id>|<reason>` (grace_period/latest reasons use a synthetic project ID like `_grace` / `_latest` since they aren't project-scoped).

Store methods (added to the STORE-001 interface):
```go
AddReference(ctx, VersionReference) error
RemoveReference(ctx, ecosystem, pkg, version, projectID string) error
CountReferences(ctx, ecosystem, pkg, version string) (int, error)
ListReferences(ctx, ecosystem, pkg, version string) ([]VersionReference, error)
```

The planner (PLAN-001) calls `AddReference`/`RemoveReference` when it computes `ADD_REFERENCE`/`DROP_REFERENCE` actions for a project's resolved dependencies.

## Inputs / Outputs
- Input: planner-computed reference deltas per project scan/resolve.
- Output: queryable reference counts per (ecosystem, package, version).

## Failure behavior
- Adding a duplicate reference (same project+version+reason) is idempotent (upsert, not error).
- Removing a non-existent reference is a no-op, not an error.

## Tests
- Two projects referencing the same version create two distinct reference records; `CountReferences` returns 2.
- Removing one project's reference leaves the other; count becomes 1, not 0.
- Removing the last reference leaves count 0 (RET-002 is responsible for what happens next).

## Acceptance criteria
- [ ] Two projects using same version create two references.
- [ ] Removing one does not make the version GC eligible (count still > 0).
- [ ] References survive a store restart (bbolt persistence).
