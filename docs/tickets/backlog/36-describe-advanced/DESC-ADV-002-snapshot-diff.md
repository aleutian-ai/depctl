# DESC-ADV-002: Snapshot diff

**Epic:** Describe — Advanced
**Status:** planned
**Depends on:** DESC-001
**Estimated size:** small

## Goal
`ragctl describe diff <old.json> <new.json>` (two saved `--json` outputs) reports what changed: packages added/removed, versions bumped, chunk counts that grew/shrank, sources added/removed per package.

## Simplicity constraints
Pure diff over two already-serialized `Report` structs — no new data gathering, no persisted history of past reports (the user saves snapshots themselves, e.g. in CI, if they want a trail).

## Acceptance criteria
- [ ] Field-by-field diff between two `Report` JSON files, grouped by package.
- [ ] A package present in one snapshot and absent in the other is reported as added/removed, not silently skipped.
