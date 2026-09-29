# COORD-003: Bulk throughput worker pool

**Epic:** Foreground JIT Isolation and Lifecycle Coordination
**Status:** built; default concurrency re-measured on corrected data (see notes) — one acceptance box (live JIT-isolation proof against a real daemon) still open
**Depends on:** COORD-001, COORD-002
**Estimated size:** medium
**Priority:** P2 — conditional on measurement, not launch-blocking

## Goal
Make an explicit, operator-triggered bulk sync (`ragctl sync` with no filter — a real project's full dependency closure) process multiple dependencies concurrently instead of strictly sequentially, using COORD-001's coordination primitives so it's safe alongside any concurrent JIT activity.

## Non-goals
- Not launch-critical — only pick this up once COORD-001/002 have shipped and real measurement (not assumption) shows bulk throughput is still worth the added complexity. The external review's framing: "Bulk jobs run concurrently within resource limits" is a P2 optimization, not a safety fix.
- No fully general-purpose scheduler or configurable priority framework — a bounded worker pool, nothing more elaborate.
- Not a fix for GitHub rate-limiting or Ollama's real concurrent-request throughput ceiling — both are unmeasured (flagged earlier this session); this ticket's default concurrency should be conservative *because* those are unknown, not because 2 is a proven optimum.

## Design
1. `RunSync`'s sequential `for len(queue) > 0` loop becomes a bounded pool of workers pulling from a mutex-protected queue (not `errgroup.SetLimit` with default cancel-on-first-error semantics — see the explicit correction below).
2. **Scoped per `RunSync` call, never a shared daemon-wide pool.** This is the design decision COORD-002 depends on for its "foreground capacity by construction" property — verify no semaphore/pool spans multiple concurrent `RunSync` invocations before this ships, since that would silently reintroduce the exact stranding bug this epic fixes, just one level down.
3. **Per-dependency failure isolation, explicitly not `errgroup`'s default behavior.** If one worker's `errgroup.Group` call returns an error, `errgroup` cancels every other worker's shared context by default — recreating a batch-wide failure in a new form, exactly the STRESS-005 finding this epic traces back to. Each worker must record its own dependency's outcome (`synced`/`failed`/`skipped`, matching today's existing counters) independently, never cancelling unrelated in-flight workers. Use a plain bounded semaphore (buffered channel) + `sync.WaitGroup`, or `errgroup` with each worker function always returning `nil` to the group itself while recording the real per-item result into a separate, mutex-protected results collector.
4. Each worker gets its own per-dependency timeout (replacing the old whole-batch `maxActionDuration` as the thing that actually bounds one dependency's work) — this is what fixes STRESS-005/epic 51's original misdiagnosis: one huge dependency (`alibaba-cloud-sdk-go`-shaped) can fail cleanly on its own budget without starving the other N-1 workers, instead of one shared deadline for the entire queue.
5. Every worker's actual generation-build work goes through COORD-001's `BuildCoordinator.Build` — same coordination as JIT, just more callers running concurrently.
6. `SyncPriority.Drain()`'s bump-to-front semantics need re-verification under a pool: "whichever worker grabs the queue next picks up the bumped item first," not "the one single loop does" — the queue itself needs its own mutex now that multiple workers pop from it concurrently.
7. `Sync.MaxConcurrency` config knob, default **2** (external review's explicit choice: conservative, exercises real concurrency without over-committing against unmeasured Ollama/network limits) — benchmark 2/3/4 against the same real fixture (`hashicorp/terraform`, this epic's own established stress-test project) before finalizing the shipped default.

## Inputs / Outputs
- Input: an untargeted `ragctl sync` against a real, large project (reuse STRESS-001/005's terraform fixture).
- Output: real wall-clock at concurrency 2, 3, and 4, compared against the sequential baseline already measured this session (2100s for 154 dependencies before hitting the old whole-batch ceiling); per-dependency failure isolation confirmed (one failing dependency never takes others down with it).

## Failure behavior
- One dependency's failure or timeout must never cancel unrelated in-flight workers — this ticket's own primary correctness requirement, tested explicitly, not just implied by "we didn't use errgroup's default."
- A worker pool that silently spans multiple `RunSync` calls (violating design point 2) is a regression against COORD-002's own guarantee, not an acceptable tradeoff for throughput.

## Tests
- N dependencies, one deliberately slow/blocked and one deliberately failing: the rest complete normally, unaffected by either.
- Concurrency actually happens: a test asserting real overlap in wall-clock between two workers' acquisition windows (not just "the code compiles with a pool").
- `SyncPriority.Drain()` still correctly prioritizes under concurrent workers (a bumped dependency is picked up promptly, not stuck behind whatever the queue's default order would have processed first).
- Re-run the real live scenario: `hashicorp/terraform`'s full untargeted sync (this epic's own established fixture) against a freshly-wiped Qdrant, at the chosen default concurrency — record real synced/failed/skipped counts and wall-clock, compared against the sequential baseline.

## Acceptance criteria
- [x] Bulk jobs run concurrently within the configured limit, verified by a real overlap test, not just code inspection.
- [x] One dependency's failure or slowness never affects another's outcome, verified explicitly.
- [ ] The worker pool is confirmed scoped per-`RunSync`-call — a live test alongside a concurrent JIT request (reusing COORD-002's own regression test shape) shows no cross-run contention.
- [x] Default `MaxConcurrency` is chosen from real benchmark numbers (2 vs. 3 vs. 5 against the terraform fixture, on corrected data), not asserted without measurement — see the 2026-09-28 bounded re-benchmark below. Kept at 2.

## Post-implementation notes
- Built: `RunSync` runs `Sync.MaxConcurrency` workers pulling from a mutex-guarded priority-aware `syncQueue`; per-dependency timeout (`dependencySyncTimeout`, 10 min) replaced the old whole-batch ceiling; per-item failure isolation (not errgroup cancel-on-error). Checked above: `TestWorkerPoolProcessesActionsConcurrently`, `TestWorkerPoolIsolatesOneFailureFromOthers`, plus `TestWorkerPoolHonorsPriorityBump` and `TestRunSyncDependencyTimeoutDoesNotWedgeOtherWorkers`.
- Benchmark at N=3/5/7 (4-minute windows, cold): throughput plateaued from N=3 (137/145/140 dependencies done). **Those numbers are not trustworthy as a basis for the default:** the same runs carried the point-ID collision (10/18/22 validation failures, all caused by it) and indexed near-empty repo-root content for monorepo submodules, so each dependency was far cheaper than a real one. Real-content full-scale figure: N=5 finished 547 of 561 dependencies in about 75 minutes (~7 per minute).
- The default is still 2 and is **still unmeasured against corrected data**; acceptance box 4 stays open until 2/3/5 are compared on real content. Box 3 (a live JIT request alongside a running bulk pool) also stays open: COORD-002's regression tests cover it in-process, but it was not exercised against a real daemon.
- Client-side: `Client.Sync` has its own 4-hour `syncTimeout` (the 35-minute long-request timeout cut off the first full run at 2100s).
- Known limit: the pool is per `RunSync` call, i.e. per project. With ambient sync (SCOPE-002) registering many projects at once, total concurrency is projects x `MaxConcurrency`; a daemon-wide cap is not built.

## Re-benchmark on corrected data, 2026-09-28

**First attempt (aborted): a full-completion 2/3/5 comparison run overnight as a background agent, killed after ~12 hours with only the first (concurrency-2) run at 160/569 dependencies.** Root-caused via macOS's own power log (`pmset -g log`), not guessed: the machine went through many real `Sleep`/`DarkWake` cycles overnight (`Entering Sleep state due to 'Idle Sleep'`, repeated roughly every 10-15 minutes), each one fully suspending the daemon and its workers — no CPU scheduling at all during sleep. The daemon's own observed per-dependency rate during the run (median 7s/dep) was healthy and would have finished the full corpus in under an hour; the slow wall-clock progress was almost entirely sleep time, not sync slowness or a scheduler problem. Killed cleanly (isolated daemons stopped by exact verified PID, isolated scratch dir removed, production `ragctl` Qdrant collection reconfirmed untouched at 1,609,940 points throughout).

**Second attempt (this measurement): a bounded 10-minute-per-level comparison instead of full completion, run live with `caffeinate -dims` holding the machine awake for the duration** — a deliberate tradeoff to get a real, same-day answer rather than waiting for another multi-hour unattended run. Methodology: three separate isolated `$HOME`s, each with its own non-default Qdrant collection name written into `config.yaml` *before* any `ragctl scan` (the exact ordering fix for the ambient-sync-into-production incident from this epic's earlier STRESS re-verification session), each scanning a fresh `hashicorp/terraform` clone (Go 1.26.8 toolchain, matching the checked-out commit's `go.mod` requirement) and running `ragctl sync --project <root>` for exactly 10 minutes before being stopped.

| Concurrency | Dependencies done (10 min) | Median time/dep | p90 time/dep |
|---|---|---|---|
| 2 | 49 of 569 | 14s | 38s |
| 3 | 36 of 569 | 24s | 76s |
| 5 | 52 of 569 | 25s | 65s |

**Reading the data honestly:** the completed-count column is noisier than a full-completion run would be — each window's tail got stuck processing the same large dependency (`cloud.google.com/go/compute`, ~29,195 chunks) at a different point depending on worker-scheduling luck, which skews a short bounded window more than it would a full corpus run. The **latency signal is not noisy and is the more trustworthy number here**: median per-dependency time roughly doubled going from concurrency 2 (14s) to 3 (24s) and 5 (25s), with p90 degrading similarly — a real, consistent sign of contention once more than ~2 workers hit the same shared resource concurrently (almost certainly Ollama's embedding throughput, exactly the unmeasured risk this ticket's own non-goals flagged from the start).

**Decision: keep the default at 2.** The completed-count data doesn't show a reliable benefit to raising concurrency, and the latency data shows a real cost. This is real, measured evidence on corrected (post-POINT-004) data, but it's a bounded-window measurement, not the full-corpus completion comparison the ticket originally called for — a full run (with `caffeinate` held for the duration, avoiding the first attempt's failure mode) would give a cleaner, less scheduling-luck-sensitive signal and is worth doing whenever a multi-hour unattended window is available. Not blocking: the bounded data already supports the conservative default the external review chose, it just doesn't upgrade "conservative and safe" to "provably optimal."
