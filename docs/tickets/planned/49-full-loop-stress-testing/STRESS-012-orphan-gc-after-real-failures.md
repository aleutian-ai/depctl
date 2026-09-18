# STRESS-012: Orphan GC after real failures

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** STRESS-006, STRESS-010 (reuses their real casualties)
**Estimated size:** small

## Goal
Run `ragctl gc --orphans` for real against the actual failed/stuck generations left behind by STRESS-006 (kill-mid-sync) and STRESS-010 (real failure injection) — the real-world counterpart to GC-001/002/003's fixture-scale tests, cleaning up genuine casualties instead of hand-constructed `domain.Generation{State: GenFailed}` fixtures.

## Non-goals
- No new orphan-detection logic — this verifies existing behavior against real casualties.

## Simplicity constraints
- Run this after STRESS-006/010 in the same session so their real casualties are still present in the store, rather than re-manufacturing failures from scratch.

## Design
1. `ragctl gc --orphans --dry-run` — confirm every real failed/stuck generation left over from STRESS-006/010 appears as a candidate, correctly reasoned (`failed` vs. `stale_nonterminal`), and nothing healthy is included.
2. `ragctl gc --orphans` (real run) — confirm all candidates are deleted (vector points by generation ID, Badger generation data, bbolt generation record), `ragctl doctor` reports clean afterward.
3. Confirm the *other*, healthy generations for the same dependencies (if any exist from a successful retry) are completely untouched — the real-world version of GC-003's own generation-ID-scoping guarantee.

## Inputs / Outputs
- Input: real failed/stuck generations from STRESS-006/010.
- Output: pass/fail on correct candidate discovery, correct complete deletion, and non-interference with healthy generations sharing the same dependency+version.

## Failure behavior
- A missed real orphan, a wrongly-deleted healthy generation, or a partial deletion under real conditions is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] Every real casualty from STRESS-006/010 is correctly discovered and deleted by `ragctl gc --orphans`.
- [ ] Any healthy generation sharing the same dependency+version is completely untouched.
- [ ] `ragctl doctor` reports clean afterward.
