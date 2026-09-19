# COORD-003: Bulk throughput worker pool

**Epic:** Foreground JIT Isolation and Lifecycle Coordination
**Status:** planned
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
- [ ] Bulk jobs run concurrently within the configured limit, verified by a real overlap test, not just code inspection.
- [ ] One dependency's failure or slowness never affects another's outcome, verified explicitly.
- [ ] The worker pool is confirmed scoped per-`RunSync`-call — a live test alongside a concurrent JIT request (reusing COORD-002's own regression test shape) shows no cross-run contention.
- [ ] Default `MaxConcurrency` is chosen from real benchmark numbers (2 vs. 3 vs. 4 against the terraform fixture), not asserted without measurement.
