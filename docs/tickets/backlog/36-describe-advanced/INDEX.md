# Epic: Describe — Advanced

Follow-on features for `ragctl describe` (`docs/tickets/planned/20-describe`) deliberately deferred out of v1 to keep the first version to a single read-only reporting pass over existing state. Each of these adds either a new data source `describe` needs to reach into, or a richer rendering mode — none require redesigning `Report`'s shape from DESC-001.

## Tickets
- [DESC-ADV-001](DESC-ADV-001-liveness-integration.md) — show source reachability (REG-007) inline in describe output.
- [DESC-ADV-002](DESC-ADV-002-snapshot-diff.md) — diff two `describe --json` snapshots to show what changed since last time.
- [DESC-ADV-003](DESC-ADV-003-interactive-html.md) — sortable/filterable HTML instead of a static table.
- [DESC-ADV-004](DESC-ADV-004-content-preview.md) — sample chunk text per source, so a human can sanity-check quality without a separate MCP query.
- [DESC-ADV-005](DESC-ADV-005-project-crosscut.md) — "which of my projects use package X" view, cutting across projects instead of only by package.
- [DESC-ADV-006](DESC-ADV-006-trust-composition.md) — visualize trust-class composition per package and flag under-curated (mostly-`unknown`) ones.
