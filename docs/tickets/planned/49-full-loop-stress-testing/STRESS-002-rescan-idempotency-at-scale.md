# STRESS-002: Re-scan idempotency at scale

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** STRESS-001 (reuses its fixture project)
**Estimated size:** small

## Goal
Run `ragctl scan` three times in a row against STRESS-001's large project, unchanged between runs, and confirm zero state drift — same resolution, same fingerprint, no duplicate `ADD_REFERENCE` actions or duplicate project registrations. Fixture-scale idempotency is already covered by existing unit tests (`TestPlanNeverWritesState` and similar); this ticket is specifically about whether that guarantee holds when there are 100+ dependencies' worth of bbolt writes per scan, not 1-2.

## Non-goals
- No change to `scan`'s own logic — this is a verification ticket, not a fix ticket, unless it finds something.

## Simplicity constraints
- Reuses STRESS-001's already-cloned project, no new fixture.

## Design
1. `ragctl scan <path>` (first run, from STRESS-001, or fresh if starting this ticket independently).
2. Capture the project's `Resolution.Fingerprint` and the full `ragctl deps` output.
3. `ragctl scan <path>` again (second run), same repo state.
4. Capture fingerprint/deps again — must be byte-identical to step 2.
5. `ragctl plan` after the second scan — must show zero `ADD_REFERENCE`/`SYNC_VERSION`/`DROP_REFERENCE` actions (everything already resolved and unchanged).
6. Third scan + plan, same assertions.

## Inputs / Outputs
- Input: STRESS-001's project, scanned three times with no changes in between.
- Output: pass/fail on fingerprint/deps stability and zero-action replanning across all three runs.

## Failure behavior
- Any drift (fingerprint changes, deps list changes, non-empty plan after the first scan) is this ticket's finding — record exactly what changed.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] Three consecutive scans of an unchanged 100+-dependency project produce identical fingerprints and dependency lists.
- [ ] `ragctl plan` after the first scan (and every scan after) shows zero actions.
