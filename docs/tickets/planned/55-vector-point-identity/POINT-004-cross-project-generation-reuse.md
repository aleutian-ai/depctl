# POINT-004: Duplicate generations from a check-then-create race, permanently unreachable by GC

**Epic:** Vector Point Identity
**Status:** done — both options built, tested (including two fail-then-pass verified regressions), full suite green under `-race`
**Depends on:** POINT-002 (its full-scale re-measurement found this)

## Problem, measured live
POINT-002's full-scale re-measurement (11 real Go modules from the terraform corpus, sharing many common dependencies) found **71.25%** of all Qdrant points are duplicates (2,943,286 total, 846,099 distinct chunk IDs) — worse than the 33.9% the `Ref: "HEAD"` fix was supposed to improve on.

Broken down by cause:
- **62.2%** (1,831,688 points): the exact same `(dependency, version)` stored under multiple different `generation` IDs.
- **9.0%** (265,499 points): different dependencies/versions whose content happens to be byte-identical (shared `LICENSE`/boilerplate across distinct real packages) — not a bug.

The 62.2% bucket is this ticket. Concrete example, confirmed via `points/count` and a payload sample: `cloud.google.com/go/appengine@v1.9.7` — one exact dependency@version — has **6,735 points across at least 5 different `generation` IDs**.

## Root cause, confirmed in code (two prior hypotheses in this ticket's history were wrong — see below)
`domain.Generation` (`internal/domain/domain.go:148-155`) is correctly modeled as global, content-identity artifact with **no `ProjectID`**, and `Store.GetActiveGeneration(ctx, ecosystem, name, backend)` (`internal/control/bbolt/active_generations.go:89-104`) is a correctly global, at-most-one-ACTIVE-per-(ecosystem,name,backend) pointer. The design intent is right. The bug is a **TOCTOU race between checking that pointer and acting on it**, with no lock covering the full sequence:

1. `computePlans` (`internal/cli/plan.go:84-92`) reads `GetActiveGeneration` as a **plain, unlocked bbolt read**, once per `RunSync` call, before any coordination happens. `planner.Plan` (`internal/planner/planner.go:90-111`) emits `ActionSyncVersion` purely off that stale snapshot.
2. The only lock in this path, `BuildCoordinator.Build`'s `singleflight.Group` (`internal/daemon/coordinator.go:68-73,117-122`, keyed on `ecosystem/name@version` — matches `generation.Create`'s identity scope, `coordinator.go:54-61`), only coalesces calls that **literally overlap in time**. Once one call returns, the key is forgotten (proven by the coordinator's own test, `TestBuildAllowsFreshAttemptAfterEarlierOneFinishes`, `internal/daemon/coordinator_test.go:83-102`) — it is not a "has this already been built" cache.
3. `generation.Create` (`internal/data/generation/generation.go:47-73`, called only from `internal/cli/sync.go:882`) performs **no reuse check of its own** — it unconditionally mints a new generation. `promote.Promote`/`PromoteGeneration` (`internal/lifecycle/promote/promote.go:37-47`) unconditionally supersedes whatever was previously active — no "is a generation for this exact version already active" guard.
4. The daemon `Scheduler` runs different projects' `RunSync` calls with **no cross-project lock at all**, deliberately (`internal/daemon/scheduler.go`'s own `execute()` comment cites epic 53/COORD-002). So two projects resolving the identical (ecosystem, name, version) — common at 11-project scale, where many terraform backend submodules share dependencies like `cloud.google.com/go/*` — can each see "not active yet," each build, each promote; only the last promotion wins the ACTIVE pointer, but the earlier N-1 generations' points were already replicated into Qdrant (`internal/data/generation/replicate.go:55` tags each point with its own `GenerationID` — genuinely separate rows, not overwrites).

**Why GC never cleans this up — structural, not "hasn't run yet":** `PlanGC` (`internal/retention/gc_planner.go:55-109`) determines eligibility per **(ecosystem, package, version) tuple**, not per generation ID:
```go
// gc_planner.go:93-98
if active, err := store.GetActiveGeneration(ctx, v.ecosystem, v.pkg, backendName); err == nil && active.Dependency.Version == v.version {
    continue // never GC-eligible
}
```
All N racing duplicates share the same version, and one of them *is* the active generation for that version. The whole tuple is therefore permanently excluded from `PlanGC` for as long as that version stays active/resolved — which, for a currently-correct dependency pin, is indefinitely. There is no generation-level distinction between "the active generation's points" and "an orphaned sibling's points for the same version," so the N-1 losing generations' points are never reachable by any GC pass, not merely delayed.

## Prior hypotheses in this ticket, corrected for the record
1. *"Generation reuse is scoped per-project, never checks other projects."* **Wrong**, retracted — the check is already global by design (see above).
2. The actual mechanism is the race described above, confirmed by tracing `BuildCoordinator`'s exact dedup semantics and `PlanGC`'s exact eligibility filter, both with file:line citations.

## Design options (both built — see Implementation)
1. **Close the race with a check-then-act re-check.** Re-run `GetActiveGeneration` immediately before `generation.Create`, inside `syncVersion` itself — if the exact version about to be built has already been promoted since the caller's plan was computed, treat the action as already satisfied and return, instead of building a redundant generation.
2. **Clean up what already exists, and whatever slips through in the future.** A new GC eligibility path, generation-scoped, independent of the existing reference/grace-period `PlanGC`: any SUPERSEDED generation whose exact `(ecosystem, package, version)` tuple also has a currently-ACTIVE generation is a provable duplicate — same content, redundant by construction — regardless of whether the version itself is still referenced by any project.
3. **Both — built.** They fix different halves of the same bug (creation-time vs cleanup-time); option 1 alone would leave the already-existing ~62% duplication in Qdrant forever (structurally unreachable by the existing reference-based GC, see Root cause above), and option 2 alone would keep letting new races create duplicates, just clean them up eventually.

## Non-goals
- No re-litigating POINT-002's `Ref: "HEAD"` fix — that fix is correct and stays; this ticket is about a race independent of it.
- No change to the deliberately-manual nature of orphan/GC triggering (epic 22's own non-goal) — the new cleanup path is a third explicit-trigger-only pass (`ragctl gc --superseded-duplicates`), matching `--orphans`' own precedent exactly, never automatic.
- No cross-project mutex at the scheduler level (a much bigger, slower lock than singleflight already provides) — the fix is a targeted re-check at the one place a redundant build is actually about to be committed.
- No change to `PlanGC`'s own reference/grace-period eligibility logic — the new duplicate-cleanup path is fully separate and never combined into the same report or deletion run, mirroring `PlanOrphanGC`'s existing precedent.

## Implementation

**Option 1 — the race fix** (`internal/cli/sync.go`, `syncVersion`): moved the existing `GetActiveGeneration` lookup (previously done only *after* build+replicate, purely to feed `validate.Run`'s drift comparison) to run *before* `generation.Create` instead. If the active generation's version already exactly matches the version about to be built, `syncVersion` returns `nil` immediately — no generation created, no build attempted. The later `validate.Run` call reuses this same `prior`/`priorManifest` rather than re-fetching, since nothing in between changes which generation was active *before* this one started.

Regression test: `TestSameDependencyAcrossProjectsSequentialRaceProducesOneGeneration` (`internal/cli/coord_cross_project_test.go`) — deliberately the *sequential*, non-overlapping shape (project A's build fully completes and returns before project B's request even begins), distinct from the pre-existing `TestSameDependencyAcrossProjectsCoalescesIntoOneRealBuild` (which only proves the *overlapping* case, already handled by singleflight). **Verified rigorously**: temporarily reverted the fix, confirmed the test fails and reproduces exactly the bug (2 generations, active pointer changes to the second one's ID), then restored the fix and confirmed it passes.

**Option 2 — the cleanup path**, new and separate from every existing GC mechanism:
- `retention.PlanSupersededDuplicateGC` (`internal/retention/superseded_duplicate_planner.go`): groups every generation by its exact `(ecosystem, package, version)` tuple; a SUPERSEDED generation in a tuple that also contains an ACTIVE one is a candidate. Deliberately **not** a tweak to `PlanGC`'s own eligibility check — `PlanGC` bails out on any `"project"`-reason reference before it ever reaches its active-generation check, so a still-referenced version (the common case here) never even reaches that logic; this needed to be a wholly separate path, keyed on generation-tuple grouping alone, no reference/grace-period involvement at all.
- Critical correctness case, verified by a dedicated test: a SUPERSEDED generation whose tuple has **no** currently-ACTIVE sibling (an older version, since superseded fleet-wide by a newer promotion) is never selected — `internal/query/search.go`'s promoted-version check (`ACTIVE` or `SUPERSEDED` both count as query-eligible) depends on that older version's own generation staying present so it remains queryable, per VALID-002's existing multi-project scenario. Only a *same-version* SUPERSEDED sibling is providably redundant.
- `gc.RunSupersededDuplicates` (`internal/lifecycle/gc/superseded_duplicates.go`): deletes generation-scoped only (`backend.Filter{Generation: id}`, Badger `DeleteGeneration(id)`, bbolt `DeleteGenerationRecord(id)`) — never version-scoped, which would also delete the ACTIVE sibling's identical-version points. No `DeleteAllReferences` call, mirroring `RunOrphans`' own reasoning: a generation that lost its promotion race never owned reference rows. Same job-keyed idempotent/restartable shape as `Run`/`RunOrphans` (RET-004's established pattern).
- Wired through as a **third**, fully separate manual GC pass — `ragctl gc --superseded-duplicates` — following `--orphans`' existing precedent exactly (epic 22's non-goal: no automatic/implicit cleanup). Full chain: `api.GCRequest.SupersededDuplicates` → `Client.GC`'s third bool → `handleGC`'s third branch → `Scheduler.RequestOrphanGC` (reused as-is — already `GCFunc`-shaped and generic, needed no new scheduler method) → `Engine.SupersededDuplicatesGC` → `cli.RunSupersededDuplicatesGC` → `retention.PlanSupersededDuplicateGC` + `gc.RunSupersededDuplicates`. Rejects `--orphans --superseded-duplicates` together (separate passes, like the existing orphans/reference-based split).

Tests: 5 planner unit tests (same-version sibling found; older-version sibling excluded; no-active-sibling excluded; multiple duplicate siblings; unrelated-dependency isolation), 3 executor tests (generation-scoped deletion only — proven against a shared-version ACTIVE sibling exactly like `RunOrphans`' own regression; failure-then-restart; already-succeeded job is a no-op), 3 CLI end-to-end tests (`--dry-run` lists without deleting; plain `gc` ignores duplicate candidates; `--orphans --superseded-duplicates` together is rejected).

**Verification**: full suite green (`go build ./...`, `go vet ./...`, `go test ./...`), and `-race` clean on every touched package (`internal/cli`, `internal/daemon`, `internal/daemon/api`, `internal/daemon/client`, `internal/retention`, `internal/lifecycle/gc`).

## Acceptance criteria
- [x] Root cause confirmed in the actual code path (file:line), not just inferred from the Qdrant-level measurement.
- [x] Decide scope: both options built.
- [x] A regression test proving two sequential (non-overlapping) `syncVersion` calls for the identical `(ecosystem, name, version)` across two projects produce exactly one generation — verified fail-then-pass, not just passing.
- [x] A regression test proving a superseded sibling generation for a still-active version is GC-eligible (via the new dedicated path) and its points get deleted, without touching the active sibling or an older, still-legitimately-superseded version.
- [ ] Re-measure the terraform corpus (or a representative multi-project subset) after the fix; the 62.2% same-dependency-multiple-generation bucket should collapse toward ~0% for new syncs, and `ragctl gc --superseded-duplicates` should clear the existing backlog.
