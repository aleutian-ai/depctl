# SEC-005: No-telemetry invariant, enforced not just claimed

**Epic:** Security hardening
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** small

## Goal
depctl's own one-pager states no customer code, embeddings, or traces are sent to Aleutian by default and no hosted account is required. That's currently true by omission — there is no telemetry code in the repo — but it's an assumption resting on nobody adding one later without noticing. The Grounded Docs comparison surfaced this concretely: that project ships real telemetry (PostHog, `telemetryEnabled: true` by default, disableable via `--no-telemetry`/`DOCS_MCP_TELEMETRY=false`), which is a legitimate, disclosed design choice on their part but underlines that "no telemetry" is a claim worth testing for depctl, not assuming.

## Non-goals
- Not adding telemetry with an opt-out — this ticket enforces the opposite: no telemetry exists, full stop, unless a future explicit decision changes that (which would itself need a new ticket, not a quiet addition).
- Not a network-sandboxing mechanism — this is a code-level/CI-level check, not a runtime firewall.

## Simplicity constraints
- A grep-based CI check (no analytics SDK imports, no known telemetry-endpoint hostnames in the codebase) plus one integration test asserting a normal `depctl sync`/`depctl serve` run makes no outbound HTTP calls beyond the ones the user's own config explicitly names (git remotes, the configured embedder endpoint, the configured vector backend endpoint) — not a new subsystem.

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
- [x] CI fails if a known analytics SDK enters the dependency graph.
- [x] A runtime test proves a normal sync/serve session makes no outbound network call beyond its own explicitly configured endpoints.
- [x] `docs/architecture.md` states this as a verified invariant, not an aspirational claim.

## Post-implementation note (2026-09-29)

Implemented as three `go test`-native checks in `internal/cli/no_telemetry_test.go` (runs under `go test ./...`/CI automatically, rather than a separate `hack/` script needing its own wiring):

1. **`TestNoTelemetrySDKInDependencyGraph`** — uses `runtime/debug.ReadBuildInfo()` to inspect the *real, actually-linked* module graph of the test binary (not just anything sitting unused in `go.sum`) against a denylist of known analytics/telemetry SDK import-path substrings (PostHog, Segment, Mixpanel, Amplitude, Sentry, Bugsnag, Rollbar, Datadog, New Relic, Honeycomb).
2. **`TestNoTelemetryHostLiteralsInSource`** — walks every `.go` file in the repo (skipping `.git`/`node_modules`/`vendor` and its own source, which must contain the denylist strings to check for them) for known telemetry-collector hostname literals, catching a hardcoded endpoint that wouldn't need a named SDK import.
3. **`TestRealSyncMakesNoUnexpectedNetworkCalls`** — the real runtime check: a genuine `syncVersion` run (real local git fixture repo — zero network I/O on the git side — real bbolt/Badger stores, and **real** `internal/embedding/ollama`/`internal/backend/qdrant` HTTP clients pointed at two `httptest` servers) with a custom `http.Transport.DialContext` installed as `http.DefaultTransport` for the test's duration, recording every single outbound TCP dial. Asserts every recorded dial landed on one of the two explicitly-configured fixture endpoints — nothing else. Verified this test actually catches a violation (not vacuous): deliberately narrowed its own allowlist to exclude one of the two legitimate endpoints and confirmed it failed with the exact expected address, then restored it.

`docs/architecture.md` updated with a dated note naming these three tests as the actual verification (module-graph absence, source-literal absence, and a real network-allowlist proof), not an aspirational claim.
