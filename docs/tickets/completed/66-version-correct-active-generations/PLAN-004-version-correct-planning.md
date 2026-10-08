# PLAN-004: Version-correct planning

**Epic:** Version-correct active generations
**Status:** done (2026-10-02)
**Depends on:** STORE-005
**Estimated size:** small

## Goal
When a project's resolved version of a dependency changes, the plan builds the new version. Live-found in `VEC-016`: moving `uuid` from v1.6.0 to v1.5.0 left v1.6.0 active and `plan` reporting "up to date", because `internal/cli/plan.go` marked a version as built whenever *any* version of the dependency was active.

## Design
- `plan.go` asks `GetActiveGeneration` for the exact resolved version (`STORE-005` makes any other question impossible).
- Every other "is this dependency built?" check answers for the project's resolved version: query's `HasActiveGeneration` and knowledge status, search, `describe`, `doctor`, `status`, and the `depctl export` connectors (which previously exported whatever version was active).

## Tests
- Plan regression: a project whose resolution moves to a version with no active generation gets `SYNC_VERSION` for it, even while another version of the same dependency is active.
- Query: `HasActiveGeneration` is false when only a different version is active.

## Post-implementation note (2026-10-02)
`internal/cli/plan.go` asks for the exact resolved version. The same version-blind check is fixed everywhere it appeared: query's `HasActiveGeneration` and knowledge status, project search, release-change lookup, `doctor`, `describe` (which now lists every active version of a package), and the `depctl export` connectors (which now export the project's own version and skip a not-yet-synced one rather than exporting whichever version happened to be active). Regression tests go through the real `plan.go` caller with a real bbolt store (`internal/cli/plan_version_test.go`), not only the pure planner.

**Verified live:** project A on v1.5.0 and project B on v1.6.0, both synced. A's search returned only v1.5.0 and B's only v1.6.0, each from its own generation. A full resync rebuilt nothing; GC removed neither while both were referenced.

- [x] Version change → `SYNC_VERSION` for the new version, through the real `plan.go` caller.
- [x] No caller reports or serves a different version than the project resolves.
