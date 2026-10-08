# DESC-ADV-005: Project cross-cut

**Epic:** Describe — Advanced
**Status:** planned
**Depends on:** DESC-001
**Estimated size:** small

## Goal
`depctl describe --by-project` (or `depctl describe project <id>`) shows the inverse cut DESC-001's fleet-wide view doesn't: for one project, every dependency it references and each one's corpus richness — versus DESC-001's default of "one row per package regardless of who references it."

## Simplicity constraints
Reuses the exact same `PackageEntry` construction as DESC-001, just filtered/grouped by `VersionReference.ProjectID` instead of deduped across all references — no new data-gathering path.

## Acceptance criteria
- [ ] Per-project view lists every referenced dependency with the same richness fields DESC-001's fleet-wide table shows.
- [ ] A project referencing a package with no active generation shows that gap explicitly (matches DESC-001's own "declared but never synced" principle).
