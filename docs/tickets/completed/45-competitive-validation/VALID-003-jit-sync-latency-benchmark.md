# VALID-003: JIT-sync cold-latency benchmark

**Epic:** Competitive Validation
**Status:** done
**Depends on:** WATCH-019 (JIT sync-on-search), WATCH-020 (priority preemption)
**Estimated size:** small

## Goal
The only evidence that `search_dependency_docs`'s JIT-sync-on-miss path (WATCH-019) actually works and is fast enough to be usable is one anecdotal live-test number from this session (~6.3s for `github.com/spf13/pflag`, 304 chunks). Turn that into a repeatable, recorded benchmark so "cold JIT sync is fast enough for a single MCP tool call" is a measured claim, not a one-time observation — and so a future regression (e.g. an accidental full-project sync sneaking back into the JIT path) is caught automatically rather than rediscovered live again.

## Non-goals
- No comparison against Grounded Docs' `scrape` latency in this ticket — that belongs to a future `context-evals`-style comparative run (see epic INDEX), not an in-repo benchmark.
- No CI-blocking latency threshold in this first pass — record and report the number; decide on a regression gate only once there's more than one data point.

## Simplicity constraints
- A benchmark-shaped Go test (`testing.B` or a plain test that logs timing, consistent with how the rest of this codebase already avoids adding a separate benchmarking framework), not a new tool.

## Design
Measure, against a real daemon and a real small-to-medium public Go module (few enough files to keep the run fast and deterministic, e.g. `github.com/spf13/pflag` again, plus one larger module for a second data point):
- Time from `search_dependency_docs` call (against a never-synced dependency) to result, split into: JIT `SyncTrigger.SyncProject` duration (clone + normalize + embed + validate + promote) and the final retry `SearchKnowledge` call's own latency.
- Record chunk count and elapsed time together, so a future run can sanity-check "did this get slower, or did the fixture module just grow."
- Run it a few times to get a rough variance sense (network-bound acquisition is inherently noisy), not a single-sample number.

Output format: a simple table in the test's own log output (or a small `docs/` note this ticket updates) — no new persistence/reporting infrastructure.

## Inputs / Outputs
- Input: a real daemon, real network access to a small public Go module's mirror.
- Output: recorded cold-JIT-sync latency numbers for at least two fixture modules of different sizes, and a short doc note (in `docs/features/query-serving.md` or `docs/architecture.md`) stating the measured range as of this ticket's close, explicitly dated so it doesn't read as a permanent guarantee.

## Failure behavior
- Network-dependent — skips (not fails) when offline, matching the existing pattern for other live-network-dependent tests in this repo (`requireGo`-style guards).

## Tests
- The benchmark itself is the test; no separate unit test needed.

## Acceptance criteria
- [x] At least two real cold-JIT-sync timing runs recorded against modules of meaningfully different size.
- [x] A dated note added to the docs recording the measured range, distinct from a design/aspirational claim.

## Post-implementation note
Implemented as `TestJITSyncColdLatencyBenchmark` (`internal/cli/jit_sync_latency_benchmark_test.go`), gated behind `RAGCTL_LIVE_BENCHMARK=1` (skips otherwise — confirmed the default `go test ./...` run still skips it and stays clean). Two real bugs in the test itself, caught and fixed before getting real numbers: (1) `requireGo`'s own `GOPROXY=off` default (this repo's other tests never need real network resolution) silently blocked module resolution — fixed by overriding `GOPROXY` back to the real proxy inside this one test; (2) the fixture used a placeholder `v0.0.0` require line instead of resolving a real version — fixed with a real `go get <module>@latest` before scanning.

**Measured results (2026-09-17, real network + real Ollama + real Qdrant, reusing the `ragctl-qdrant` container from this session's earlier live testing):**
- `github.com/spf13/pflag` (small): 5.76s sync + 168ms search = 5.93s total, 10 chunks.
- `github.com/stretchr/testify` (medium): 7.94s sync + 123ms search = 8.07s total, 10 chunks.

Both consistent with the original anecdotal ~6.3s pflag number from earlier in this session. Recorded in `docs/features/query-serving.md`, explicitly dated as a measured range, not a permanent guarantee.
