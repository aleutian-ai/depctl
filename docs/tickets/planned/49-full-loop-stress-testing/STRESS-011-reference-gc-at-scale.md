# STRESS-011: Reference-based GC at scale

**Epic:** Full-Loop Stress Testing
**Status:** planned
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
- [ ] 20+ real, grace-expired candidates are all correctly discovered and reported by `--dry-run`.
- [ ] A real `gc` run deletes all of them cleanly, with `ragctl doctor` reporting clean afterward.
- [ ] Still-referenced dependencies are completely unaffected.
