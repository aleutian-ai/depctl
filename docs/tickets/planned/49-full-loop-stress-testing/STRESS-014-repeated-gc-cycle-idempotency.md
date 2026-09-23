# STRESS-014: Repeated GC cycle idempotency

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** STRESS-011, STRESS-012 (runs after both, when nothing new is eligible)
**Estimated size:** small

## Goal
Run `ragctl gc` and `ragctl gc --orphans` several times back-to-back, with nothing new eligible after the first successful run, and confirm each subsequent run is a fast, clean no-op — no wasted work, no spurious errors, no re-attempted deletions of already-gone content (the idempotent-job-resume machinery, `gc.JobID`/`orphanJobID`, exercised under real repeated invocation rather than a single simulated resume in the unit tests).

## Non-goals
- No new idempotency logic — this verifies existing `JobSucceeded`-short-circuit behavior under real repeated use.

## Simplicity constraints
- Run immediately after STRESS-011/012 leave the store in a "just cleaned" state — no new fixture needed.

## Design
1. Immediately after STRESS-011/012 complete, run `ragctl gc` again — confirm "nothing eligible for garbage collection", fast (sub-second-to-few-seconds, not re-scanning/re-deleting anything), zero errors.
2. `ragctl gc --orphans` again — same assertions.
3. Repeat both 3+ times.
4. `ragctl doctor` after the repeated cycles — confirm still clean.

## Inputs / Outputs
- Input: a store already fully cleaned by STRESS-011/012.
- Output: pass/fail on fast, clean, error-free no-ops across repeated runs.

## Failure behavior
- Any error, unexpected candidate, or meaningfully slow repeated run (suggesting re-scanning work that shouldn't be happening) is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] Repeated `gc`/`gc --orphans` runs against an already-clean store are fast, error-free no-ops, 3+ times in a row.
- [ ] `ragctl doctor` remains clean throughout.
