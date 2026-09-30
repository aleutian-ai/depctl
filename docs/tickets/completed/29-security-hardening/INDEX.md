# Epic: Security Hardening

**All five done, 2026-09-29** — pulled forward from backlog ahead of release, at the user's explicit request. Establishes the security invariants the project promises: local-first defaults, provenance/trust metadata on all retrieved content, explicit "this is data not instructions" labeling for agents, bounded fetch resource use, and a hard guarantee that fetched dependency code is never executed.

## Tickets

- [SEC-001](SEC-001-source-trust-metadata.md) — **done** (doc-drift fix — already fully implemented and tested; the ticket's own tracking had just never been updated). `TrustClass` on every `KnowledgeObject`, enforced by `Validate()`.
- [SEC-002](SEC-002-prompt-injection-labeling.md) — **done** (doc-drift fix, same story). A shared `securityNote` disclaimer on every knowledge-returning MCP tool response.
- [SEC-003](SEC-003-fetch-limits.md) — **done.** Scoped down from the original five config limits to the three that bound real, currently-existing external fetches (no website-acquisition/decompression code exists yet for the other two) — new shared `internal/httplimit` package, applied to every real fetch site found (including one genuinely unbounded read in `internal/registry/discover`), plus a new git-mirror on-disk size cap.
- [SEC-004](SEC-004-no-downloaded-code-execution.md) — **done.** `requireRegisteredProjectRoot` guards the real resolver-execution boundary (deliberately not inside `internal/executil` itself, which also serves unrelated git/registry operations); audited and documented that acquisition/normalization never executes fetched content as live code, with a new lasting CI check against the two extraction scripts that do shell out.
- [SEC-005](SEC-005-no-telemetry-invariant.md) — **done.** Three `go test`-native checks: no analytics SDK in the real module graph, no telemetry-hostname literals in source, and a real `syncVersion` run proving every outbound dial lands only on its own explicitly-configured endpoints.
