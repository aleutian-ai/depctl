# COORD-001: Lifecycle coordination contract

**Epic:** Foreground JIT Isolation and Lifecycle Coordination
**Status:** done
**Depends on:** none
**Estimated size:** medium
**Priority:** P0 — launch-critical

## Goal
Define and implement the minimum coordination that makes concurrent, independent generation builds safe: GC must never delete a generation an active build still needs, and two concurrent requests for the *identical* generation must join one build rather than race to produce two. This ticket is the safety contract itself — [COORD-002](COORD-002-cross-project-jit-isolation.md) wires it into the real sync path.

## Non-goals
- Not the JIT-vs-bulk wiring itself (that's COORD-002) — this ticket only builds and proves the coordination primitives in isolation.
- Not per-project registry-source-override correctness (epic 38) — see the epic INDEX's non-goals. Work identity here matches `generation.Create`'s existing scope (`domain.DependencyVersion` — ecosystem, dependency name, version) exactly, not more, not less.
- Not a general-purpose distributed lock service — everything here is in-process, matching the daemon's existing single-process design.

## Design

### 1. Build/GC gate: `sync.RWMutex`
Replace `Scheduler.global sync.Mutex` with `sync.RWMutex`. Every build (bulk or JIT, any project) takes `RLock()`/`RUnlock()` for the duration of *everything GC must be excluded from* — see the audit below, this is not just the network acquisition/embedding steps. GC keeps calling `Lock()`/`Unlock()` exactly as it does today — the call site in `executeGC` doesn't change, only the type does.

**Explicit limit, stated rather than assumed:** Go's `RWMutex` guarantees a waiting writer (GC) isn't overtaken forever by a continuous stream of new readers (new builds) — but it does **not** bound how long GC waits if builds already holding `RLock` are individually slow. A single build legitimately taking many minutes (a genuinely huge dependency, per STRESS-005's `alibaba-cloud-sdk-go` finding) still makes GC wait that long. This is an accepted, documented tradeoff, not a bug this ticket needs to additionally solve — GC waiting for real in-progress work to finish is correct behavior, not starvation.

### 2. Audit: what must `RLock` actually cover?
Read `internal/data/generation`'s `Create`, `Build`, `Replicate`, and whatever promotes a generation to `GenActive` (`internal/lifecycle/promote`), plus `internal/cli/sync.go`'s `ActionAddReference`/`ActionDropReference` handling, and answer concretely: does GC's own read of `references`/`active_generations` state race against *any* of these, not just the acquisition/embed steps? The external review flagged this precisely: "verify that the read lock covers every operation GC must exclude, including publication and reference-state changes." Write the answer down as this ticket's own design note before implementing — if `ActionAddReference`/promotion are *not* already covered by whatever `RLock` scope is chosen, widen it; don't assume "they're plain bbolt writes" is sufficient without checking bbolt's own transaction guarantees against what GC's planner actually reads.

### 3. Keyed request coalescing — not just a keyed mutex
A plain per-key mutex only serializes duplicate attempts; it doesn't make the second caller reuse the first caller's result — a second caller that acquires the lock after the first releases it would still redundantly rebuild. Use `golang.org/x/sync/singleflight` (already reachable via the existing `golang.org/x/sync` dependency), keyed on `domain.DependencyVersion` matching `generation.Create`'s own existing identity scope exactly. A concurrent second request for the same key joins the first's in-flight call and gets its actual result, rather than independently building and potentially publishing a competing generation.

### 4. Cancellation and failure release
A build whose context is cancelled (MCP tool timeout, daemon shutdown) or that fails must release its `singleflight` key and its `RLock` cleanly, and must never leave a generation in a state that looks promotable — this is already the existing lifecycle guarantee (`GenFailed`, VALID-001/FIX-001's work this session), but must be explicitly re-verified under the new coordination: a cancelled *joiner* (a second caller waiting on someone else's in-flight singleflight call) must also get a clean, correctly-typed error, not a hang or a misleading partial result.

## Inputs / Outputs
- Input: concurrent calls into the coordination layer from COORD-002's wiring (real usage) and this ticket's own direct unit tests (synthetic).
- Output: a `BuildCoordinator`-shaped component (exact name TBD during implementation) exposing roughly: `func (c *BuildCoordinator) Build(ctx context.Context, key domain.DependencyVersion, fn func(context.Context) error) error` — internally taking `RLock`, running `fn` through `singleflight.Group.Do`, releasing on completion or cancellation — and a `func (c *BuildCoordinator) ExcludeForGC(ctx context.Context) (release func(), err error)` GC calls before planning deletions.

## Failure behavior
- A build that fails must release its coordination state and report a typed, existing error (`GenFailed` path) — never silently swallowed.
- A GC call that can't acquire the exclusive gate within a reasonable bound should report that clearly (e.g. via the existing GC result/error shape) rather than hang the caller indefinitely — exact bound TBD, informed by real measurement, not guessed here.

## Tests
- Two concurrent `Build` calls for the *identical* key: exactly one real build happens, both callers get the same result, no duplicate generation is created (`depctl gc --dry-run` or a direct store check afterward shows one generation, not two).
- `ExcludeForGC` blocks until every currently-`RLock`-holding build finishes, and a *new* `Build` call made while `ExcludeForGC` is pending/held blocks until GC releases — proving the RWMutex writer-priority property holds under real concurrent goroutines, not just in isolation.
- A cancelled build releases its `singleflight` key promptly — a second caller for the same key started immediately after cancellation gets its *own* fresh attempt, not stuck waiting on a dead one.
- The RLock-scope audit's conclusion (step 2 above) gets a concrete regression test: GC racing against whatever operation was found to need coverage (reference mutation, promotion, or both) must never delete a generation that operation just made active/referenced.

## Acceptance criteria
- [x] `RLock`/`Lock` scope is documented precisely (what's covered, what isn't, and why) in the code, not just this ticket.
- [x] Two concurrent requests for the identical generation produce exactly one build, both callers correctly served.
- [x] GC provably excludes every concurrent build, and is provably not starved forever by continuous new builds, both proven by tests using real concurrent goroutines (not just sequential simulation).
- [x] A cancelled or failed build's coordination state is fully released, verified for both the original caller and any joiners.

## Post-implementation note

Implemented as `internal/daemon/coordinator.go`'s `BuildCoordinator`: `sync.RWMutex` gate (`Build`/`ProtectFromGC` take `RLock`, `ExcludeForGC` takes `Lock`) plus `golang.org/x/sync/singleflight` keyed on `domain.DependencyVersion` (matching `generation.Create`'s own identity scope exactly, deliberately excluding source/registry config — see the epic's non-goals).

The RLock-scope audit (design step 2) resolved concretely, not just in theory: read `retention.PlanGC` directly and confirmed it reads *both* the `references` bucket (via `ListAllReferences`/`ListReferences`) *and* `active_generations` (via `GetActiveGeneration`). Promotion happens inside `syncVersion`'s own pipeline, already covered by `Build`. But `ActionAddReference`/`ActionDropReference` are separate actions that mutate the `references` bucket directly and were *not* covered by wrapping only `syncVersion` — this is why `ProtectFromGC` exists as its own method (no build-identity coalescing, just the same GC-exclusion `RLock`), not something originally planned before the audit.

One real design correction made while implementing, not before: `Build` originally was going to take a `context.Context` and use it to run `fn`. Caught before shipping that this would let one caller's timed-out MCP request cancel a `singleflight`-shared build other callers still needed the result of. `Build` takes no `ctx` at all — the caller (COORD-002's `execute()`) is responsible for closing `fn` over a context independent of any single request's cancellation, mirroring the existing `context.WithoutCancel(s.base)` pattern. `ExcludeForGC` similarly takes no `ctx`/timeout — a cancellable version was drafted and rejected: if the wait were cancelled via `ctx.Done()` while a background goroutine was still blocked acquiring `gate.Lock()`, that goroutine would eventually acquire the lock and never release it, permanently wedging the coordinator. Blocking unconditionally (matching `s.global.Lock()`'s original, equally unconditional behavior) is simpler and correct by construction.

7 tests in `coordinator_test.go`, all passing under `-race`, re-run 5x with zero flakes: `TestBuildKeyDistinguishesDependencies`, `TestBuildCoalescesConcurrentCallersForSameKey` (20 concurrent callers, exactly one real build), `TestBuildAllowsFreshAttemptAfterEarlierOneFinishes`, `TestBuildPropagatesErrorAndAllowsRetry`, `TestProtectFromGCExcludesGC`, `TestExcludeForGCWaitsForInFlightBuildsAndBlocksNewOnes` (proves both the wait-for-in-flight-builds property and the writer-priority property together).
