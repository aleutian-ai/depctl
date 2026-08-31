# Epic: Knowledge Registry

Answers "where can I obtain trustworthy knowledge for this package/version?" — separate from the dependency resolver, which answers "what package/version does this project use?" The registry is data (YAML manifests), not code, so the community can add packages without touching Go internals.

## Tickets
- [REG-001](REG-001-manifest-schema.md) — Define the KnowledgePackage YAML schema + JSON Schema validator.
- [REG-002](REG-002-registry-loader.md) — Load and merge manifests from built-in/user/project sources with priority.
- [REG-003](REG-003-registry-matcher.md) — Match ecosystem+package identity to a manifest's sources.
- [REG-004](REG-004-seed-registry.md) — Hand-author 6 real manifests to validate the format end-to-end.
