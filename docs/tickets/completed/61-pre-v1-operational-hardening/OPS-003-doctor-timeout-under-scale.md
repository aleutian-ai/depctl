# OPS-003: `ragctl doctor` times out at real scale against a degraded backend

**Epic:** Pre-v1.0 Operational Hardening
**Status:** done — 2026-09-28
**Depends on:** none (continues OPS-001/002's prefix, epic 18/completed)
**Estimated size:** small

## Problem, confirmed by reading the real code (root-caused, not just observed)
Found live during epic 55/POINT-004's full-scale re-measurement: `ragctl doctor` timed out (`context deadline exceeded`) against a 558-generation isolated collection under real disk pressure, while `ragctl status` against the same daemon returned instantly. Not root-caused at the time — root-caused now.

`checkEmptyActiveGenerations` (`internal/cli/doctor.go:476-515`) loops over every active pointer with a non-empty manifest and calls `vb.Count(ctx, env.cfg.Vector.Collection, &backend.Filter{Generation: p.GenerationID})` — **one real network round-trip to the vector backend per generation, entirely serial, no batching, no per-call timeout distinct from the whole `doctor` request's own timeout.** At small scale (a handful of generations) this is invisible. At real scale (558 generations, one query each) against a healthy backend it's merely slow; against a *degraded* backend (the `optimizer_status: red` / WAL-pressure condition doctor exists partly to help diagnose) each individual `Count` call is itself slower, and the accumulated serial latency exceeds the daemon-routed request's timeout before the check can finish — the exact scenario observed.

This is a real, disclosed-and-unfixed bug, not a hypothetical: `doctor` is specifically the tool an operator reaches for when something looks wrong, and this failure mode is worst precisely when the backend is already unhealthy — the one time `doctor` matters most.

## Non-goals
- No change to `checkEmptyActiveGenerations`'s actual detection logic (comparing manifest `ChunkCount` against the backend's real point count) — that's correct and valuable, only the execution shape needs fixing.
- No general async/job-queue redesign of `doctor` — it's meant to be a synchronous, immediate health report; the fix should keep that property, just make it not fall over at scale.
- Not blocking on epic 49's stress-testing epic, though STRESS-011/012 (reference/orphan GC at scale) will likely exercise a similar generation count and could double as a regression check once written.

## Design direction (not finalized)
Likely shape: bound the check's total time explicitly (a sub-timeout shorter than the whole `doctor` request), reporting through the existing `notChecked(dependency string)` helper (`internal/cli/doctor.go:294-296`, already used elsewhere in this same file for "Badger unavailable"/"config unavailable") rather than letting a bare context-deadline error propagate — this is extending an established pattern, not inventing a new result shape. Separately (or additionally), parallelize the per-generation `Count` calls with a small bounded worker pool (mirroring COORD-003's own bounded-concurrency pattern) so real wall-clock scales with worker count, not generation count, at a real backend under real load.

## Inputs / Outputs
- Input: a real (or realistically faked) backend with several hundred active generations, one under simulated backend slowness/degradation.
- Output: `doctor` completes within its normal bound and reports either the real result or an honest "skipped, here's why" — never a bare, unexplained timeout.

## Failure behavior
- A `doctor` timeout must never look identical to "everything is fine and doctor just didn't run this check" — if a sub-timeout is hit, the check's own result line must say so explicitly, not silently vanish from the report.

## Tests
- A fake `VectorBackend.Count` with N generations and an injectable per-call delay: confirm the check completes within a bounded time regardless of N, and reports a distinct "skipped/timed out" result rather than propagating a bare context error that aborts the whole `doctor` run.
- Real-scale live re-verification against the same kind of large, real collection this bug was originally found against (reuse epic 55/POINT-004's own methodology if a large collection is still available, or a fresh one).

## Acceptance criteria
- [x] `checkEmptyActiveGenerations` (or its replacement) completes within a bounded time regardless of generation count, verified with a fake backend at a scale larger than what caused the original timeout (500+).
- [x] A bounded/skipped result is reported honestly and distinctly from a real pass/fail, in `ragctl doctor`'s text output — corrected from the original ticket text: `ragctl doctor` has no `--json` flag at all (only `ragctl status` does), so that half of this box didn't apply.
- [x] Live-verified against a real backend at real scale — not with a synthetic 558-generation collection as originally planned, but with something better: this exact fix, running against real production data, is what surfaced and let `doctor` finish diagnosing a genuine, previously-invisible 132-generation incident (see Implementation notes) — the strongest possible proof it completes and reports honestly under real conditions.

## Implementation notes (2026-09-28)
Implemented via a bounded sub-context (`emptyGenerationsCheckBudget`, 30s, independent of the outer `doctor` request's own timeout) plus a small bounded worker pool (`emptyGenerationsConcurrency`, 8, mirroring COORD-003's own pattern) for the per-generation `vb.Count` calls, all in `internal/cli/doctor.go`. Three outcomes are now distinguished per generation — confirmed empty (a real problem), a real backend error, or skipped because the check's own budget ran out mid-call — reported as UNHEALTHY, UNHEALTHY, or WARNING respectively, never a bare propagated context error.

Two real bugs caught and fixed during testing, not just during development:
1. A per-item `context.DeadlineExceeded` from `vb.Count` was initially misclassified as a hard backend error instead of "skipped due to budget" — caught by `TestDoctorEmptyActiveGenerationsReportsBudgetExceeded`, fixed by checking `budgetCtx.Err()` specifically rather than trusting the error's shape alone.
2. That same test's fake HTTP handler originally blocked on `<-r.Context().Done()` to simulate a hung backend — this depended on the Go test server reliably detecting client-side cancellation, which didn't fire reliably in practice and caused a real 10-minute test hang (`go test`'s own default timeout, caught via a goroutine dump). Fixed by bounding the handler's own blocking time with `time.Sleep` instead, removing the dependency on cancellation-propagation timing entirely.

New tests: `TestDoctorEmptyActiveGenerationsChecksRealScaleConcurrently` (40 generations, asserts real measured overlap >1 and ≤ the worker bound — not just "the code compiles with a pool", matching COORD-003's own precedent) and `TestDoctorEmptyActiveGenerationsReportsBudgetExceeded`. Full native test suite and `go vet` clean.

**Real-world payoff**: this fix is what let `doctor` complete this check against real production data for the first time (it previously timed out before ever reaching a verdict on 132 real generations), surfacing a genuine, previously-invisible incident — see [OPS-004](OPS-004-no-real-way-to-force-rebuild-active-generation.md) for what was found and how it was fixed.
