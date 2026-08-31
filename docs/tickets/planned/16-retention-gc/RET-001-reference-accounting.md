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
- [x] Two projects using same version create two references.
- [x] Removing one does not make the version GC eligible (count still > 0).
- [x] References survive a store restart (bbolt persistence).

## Post-implementation note
This ticket rewrote the interim `VersionReference` model epic 15 (PLAN-001..003) shipped as a deliberate simplification — its own post-implementation note flagged this exact gap. Key shape changed from `<project-id>|<ecosystem>|<package>` (one row per project+dependency, current version only) to RET-001's `<ecosystem>|<package>|<version>|<project-id>|<reason>` (multiple reasons, multiple projects, multiple simultaneously-referenced versions per dependency — what RET-002/003 actually need). `AddReference`/`RemoveReference`/`CountReferences`/`ListReferences` match the ticket's method list exactly; two extra methods were added because something has to answer questions RET-001's own method list doesn't cover: `ListProjectReferences(ctx, projectID)` (full bucket scan, filtered to `Reason == "project"`) is what the planner (PLAN-001) diffs a fresh `Resolution` against — project ID is the key's *last* segment, not a prefix, so this can't be a prefix seek; `ListAllReferences(ctx)` (full bucket scan, no filter) is what RET-003's `PlanGC` uses to discover the GC candidate set, since the `dependency_versions` bucket its design assumes was never built (see STORE-001's own gap note) — every `(ecosystem, package, version)` a project has ever resolved to already has at least one reference row, making this bucket the closest thing to that enumeration that actually exists in this codebase. Both scans are full-bucket, not prefix-scoped, but stay within RET-001's own "no reference-count caching layer... small keyspace" simplicity constraint.
