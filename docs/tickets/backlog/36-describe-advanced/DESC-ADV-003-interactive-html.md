# DESC-ADV-003: Interactive HTML

**Epic:** Describe — Advanced
**Status:** planned
**Depends on:** DESC-001
**Estimated size:** medium

## Goal
Upgrade `--html`'s static table to a sortable/filterable view (by ecosystem, trust class, active/stale) — still a single self-contained file, no server.

## Simplicity constraints
Vanilla JS embedded inline in the same template, operating on the same `Report` data already serialized into the page (e.g. as an embedded JSON blob) — no build step, no framework, no external assets, consistent with DESC-001's "no JS framework" constraint (the interactivity is small enough not to need one).

## Acceptance criteria
- [ ] Table sortable by any column.
- [ ] Filterable by ecosystem and trust class.
- [ ] Still a single file, no network requests, opens correctly from `file://`.
