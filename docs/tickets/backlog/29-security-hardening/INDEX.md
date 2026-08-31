# Epic: Security Hardening

Establishes the security invariants the project promises: local-first defaults, provenance/trust metadata on all retrieved content, explicit "this is data not instructions" labeling for agents, bounded fetch resource use, and a hard guarantee that fetched dependency code is never executed.

## Tickets

- [SEC-001](SEC-001-source-trust-metadata.md) — Add `TrustClass` to `KnowledgeObject` and enforce it's always populated.
- [SEC-002](SEC-002-prompt-injection-labeling.md) — Label MCP retrieval results as data, not instructions.
- [SEC-003](SEC-003-fetch-limits.md) — Configurable size/redirect/decompression limits on Git and HTTP fetches.
- [SEC-004](SEC-004-no-downloaded-code-execution.md) — Structural guarantee that fetched repo code is never executed and resolver commands only run against registered project roots.
