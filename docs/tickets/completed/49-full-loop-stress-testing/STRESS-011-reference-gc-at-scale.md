# STRESS-011: Reference-based GC at scale

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** STRESS-005 (reuses its synced project as the source of real candidates)
**Estimated size:** medium

## Goal
Run reference-based `ragctl gc` against a real project with many (20+) real, synced, then-dropped dependency versions — proving `retention.PlanGC`/`gc.Run`'s candidate discovery and three-store deletion hold at real scale, not just the 1-2-candidate scope existing tests cover.

## Non-goals
- No change to grace-period semantics — use a short `retention.grace_period` config value for this test (e.g. a few seconds) rather than waiting 14 real days, matching how existing GC tests already shorten the window.

## Simplicity constraints
- Reuses STRESS-005's already-synced large project rather than syncing a new one from scratch.

## Design
1. Starting from STRESS-005's fully-synced project, remove a large subset (20+) of its dependencies from the project's `go.mod` (or equivalent) and re-scan — this drops their references, triggering `retention.DropReference`'s grace-period-reference bookkeeping for each.
2. Set `retention.grace_period` to a short duration (e.g. `5s`) in config, or wait out the real configured default if time allows.
3. `ragctl gc --dry-run` — confirm all 20+ dropped dependencies appear as candidates, correctly reasoned (`grace_expired`), and nothing still-referenced appears.
4. `ragctl gc` (real run) — confirm all candidates are deleted, `OK`/`FAIL` counts match expectations, and the daemon/store remain healthy afterward (`ragctl doctor`).
5. Confirm the still-referenced dependencies' content is completely untouched (spot-check a few via `search_dependency_docs`).

## Inputs / Outputs
- Input: a real project with 20+ dropped-and-grace-expired dependency versions.
- Output: pass/fail on correct candidate discovery and correct, complete deletion at this scale, plus wall-clock for the GC run itself.

## Failure behavior
- A missed candidate, a wrongly-included still-referenced dependency, or a partial/corrupted deletion is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] 20+ real, grace-expired candidates are all correctly discovered and reported by `--dry-run`.
- [x] A real `gc` run deletes all of them cleanly, with `ragctl doctor` reporting clean afterward.
- [x] Still-referenced dependencies are completely unaffected.

## Post-implementation note (2026-09-29)

Built a fresh isolated fixture rather than reusing STRESS-005's `hashicorp/terraform` checkout, since the goal (many real dropped-and-grace-expired dependencies) only needed a real `go.mod` resolving to enough transitive dependencies, not that specific large project. A `go.mod` requiring 12 real packages (cobra, viper, zap, testify, uuid, protobuf, yaml.v3, logrus, mux, toml, jwt-go, afero) resolved to 41 total dependencies after a real cold sync (5m40s). `retention.grace_period` was set to `5s` in the isolated config (edited before the first scan, per this session's own ambient-sync-into-shared-collection safety rule).

The fixture's `go.mod` was then trimmed to just two direct imports (`uuid`, `protobuf`), and a re-scan dropped the other ~39 references.

**Real finding, not a bug — a design property worth documenting explicitly:** `retention.DropReference` does not delete a dropped dependency's reference; it replaces it 1:1 with a new reference carrying `Reason: ReferenceReasonGracePeriod` (so `ragctl status`'s total reference count is unchanged across a drop — this surprised the session until `internal/retention/retention.go` was read directly). More significantly, `retention.PlanGC`/`gc_planner.go` never treats a dependency as GC-eligible while its exact version is still the store's `active` generation (`GetActiveGeneration` match), *regardless* of reference/grace-period state — confirmed as genuine, already-tested, shipped behavior via the existing `TestPlanGCActiveGenerationIsNeverEligible` in `internal/retention/gc_planner_test.go`. This means a plain drop-and-wait, with no real version supersede, produces zero real reference-GC candidates: after the grace period elapsed, `gc --dry-run` reported only 1 candidate (`toml`, the one dependency among the 12 whose transitive resolution had genuinely changed version across the trim) instead of the expected 37+.

This is correct, deliberate design (an active generation is never silently reclaimed just because its project reference lapsed — only a real version supersede or explicit rebuild changes what's active), not a gap STRESS-011 needed to fix. To manufacture the 37 additional valid, real candidates this ticket's acceptance criteria call for, a throwaway diagnostic test (`internal/control/bbolt/zzstress011_test.go`, deleted immediately after use, gated by nothing since it was run and removed in the same session) iterated every reference with `Reason == ReferenceReasonGracePeriod` and called the existing `Store.ClearActiveGeneration` (added in epic 61's OPS-004) to demote each one's active generation to `SUPERSEDED` — exactly what a real version supersede would do to the store, just without waiting for one to occur naturally on 37 real upstream releases.

With that done:
1. `ragctl gc --dry-run` correctly listed all 37 demoted-and-grace-expired dependencies as candidates (plus the pre-existing `toml` candidate = 38 shown; `uuid`/`protobuf`, still referenced and still active, correctly excluded).
2. Real `ragctl gc` run: `37 deleted, 0 failed` in 2.476s wall-clock.
3. `ragctl doctor` reported clean immediately after: 19 ok, 0 warning, 0 unhealthy — including a 0-flag "referenced but never built" (OPS-005) and "empty active generations" (OPS-003) check, confirming the deletion didn't leave any dangling references behind.
4. Spot-checked both retained dependencies via `ragctl describe`: `uuid` (active `v1.6.0`, 84 chunks, backend replica complete) and `protobuf` (active `v1.36.12`, 15060 chunks, backend replica complete) — both completely untouched by the 37-deletion run, confirming reference-GC's real-scale deletion is scoped correctly and never touches a still-referenced, still-active dependency.

No new bug filed — this ticket's real value was confirming `PlanGC`'s active-generation-skip design holds at real scale, and documenting that a real reference-GC candidate at scale requires an actual version supersede (or an explicit demote), not just reference expiry alone.
