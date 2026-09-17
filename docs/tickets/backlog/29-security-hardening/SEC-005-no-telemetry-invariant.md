# SEC-005: No-telemetry invariant, enforced not just claimed

**Epic:** Security hardening
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
ragctl's own one-pager states no customer code, embeddings, or traces are sent to Aleutian by default and no hosted account is required. That's currently true by omission — there is no telemetry code in the repo — but it's an assumption resting on nobody adding one later without noticing. The Grounded Docs comparison surfaced this concretely: that project ships real telemetry (PostHog, `telemetryEnabled: true` by default, disableable via `--no-telemetry`/`DOCS_MCP_TELEMETRY=false`), which is a legitimate, disclosed design choice on their part but underlines that "no telemetry" is a claim worth testing for ragctl, not assuming.

## Non-goals
- Not adding telemetry with an opt-out — this ticket enforces the opposite: no telemetry exists, full stop, unless a future explicit decision changes that (which would itself need a new ticket, not a quiet addition).
- Not a network-sandboxing mechanism — this is a code-level/CI-level check, not a runtime firewall.

## Simplicity constraints
- A grep-based CI check (no analytics SDK imports, no known telemetry-endpoint hostnames in the codebase) plus one integration test asserting a normal `ragctl sync`/`ragctl serve` run makes no outbound HTTP calls beyond the ones the user's own config explicitly names (git remotes, the configured embedder endpoint, the configured vector backend endpoint) — not a new subsystem.

## Design
1. **Static check**: a `hack/`-script or CI step that fails if the module graph ever pulls in a known analytics SDK (PostHog, Segment, Mixpanel, etc.) or if any Go source string-literals a known telemetry-endpoint hostname.
2. **Runtime check**: extend an existing offline/isolated-network test (the same isolation harness `internal/cli`'s tests already use) to run a representative `sync`/`serve` session behind a proxy or DNS-deny that only allow-lists the test's own configured endpoints (fixture git remote, fake embedder, fake vector backend), asserting no other outbound connection is attempted.

## Inputs / Outputs
- Input: the existing codebase and its dependency graph.
- Output: a CI-enforced invariant plus a dated `docs/architecture.md` note stating what's actually verified (module-graph absence of analytics SDKs, and a runtime network-allowlist test), not just claimed.

## Failure behavior
- A future PR that adds telemetry would fail this check loudly at CI time — the intended outcome, forcing an explicit decision and doc update rather than a silent addition.

## Tests
- The static and runtime checks described above are themselves the tests.

## Acceptance criteria
- [ ] CI fails if a known analytics SDK enters the dependency graph.
- [ ] A runtime test proves a normal sync/serve session makes no outbound network call beyond its own explicitly configured endpoints.
- [ ] `docs/architecture.md` states this as a verified invariant, not an aspirational claim.
