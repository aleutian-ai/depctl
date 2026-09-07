# DESC-ADV-006: Trust composition

**Epic:** Describe — Advanced
**Status:** planned
**Depends on:** DESC-001, SEC-001
**Estimated size:** small

## Goal
Surface each package's trust-class *composition* (what fraction of its sources are `repository` vs `official` vs `unknown`) and flag packages that are entirely `unknown` (e.g. everything synced via REG-005's no-manifest fallback) as under-curated, so a human knows where to spend registry-authoring effort next.

## Simplicity constraints
Purely a rendering/aggregation pass over `PackageEntry.Sources` (already carries `TrustClass` per DESC-001) — no new data gathering.

## Acceptance criteria
- [ ] Each package row/section shows its trust-class breakdown.
- [ ] A dedicated `ragctl describe --gaps` view lists only packages with zero non-`unknown` sources, as a prioritized curation to-do list.
