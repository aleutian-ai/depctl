# STRESS-012: Orphan GC after real failures

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29, covered by STRESS-010's own live session rather than a separate run
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
- [x] Every real casualty from STRESS-010 is correctly discovered and deleted by `ragctl gc --orphans`.
- [x] Any healthy generation sharing the same dependency+version is completely untouched.
- [x] `ragctl doctor` reports clean afterward.

## Post-implementation note (2026-09-29)

Covered live during STRESS-010's own session rather than as a separate run — its three real `FAILED` casualties (`github.com/spf13/cobra`, `github.com/google/uuid`, `golang.org/x/sys`) were still present in the store, exactly matching this ticket's own "Simplicity constraint" (run right after, don't re-manufacture failures).

1. `ragctl gc --orphans --dry-run` correctly listed all three, each reasoned `failed` (the genuine `FAILED`-state case — see STRESS-006's own note for the separate `ACQUIRING`/`INDEXING` "stale, not yet 24h old" case, which self-recovered via `--rebuild` before ever aging into orphan-GC eligibility, so its exact reason label wasn't separately re-confirmed here; `internal/lifecycle/gc`'s own fixture-scale tests already cover that label).
2. A real `ragctl gc --orphans` run deleted all three cleanly: `3 deleted, 0 failed`. `ragctl doctor` reported clean immediately after: 19 ok / 0 warning / 0 unhealthy.
3. **Generation-ID-scoped deletion confirmed for real, not just at fixture scale**: by the time this ran, all three dependencies also had a *second*, successful generation from their own `--rebuild` recovery (same dependency+version, different generation ID) — `doctor`'s "active generations" count (5, including the two other real dependencies synced incidentally during STRESS-010's kill-timing attempts) was unaffected by the deletion of the three old `FAILED` generations. The orphan-GC pass correctly deleted only the specific stale generation IDs, never touching the healthy, active ones sharing the same dependency+version — exactly the real-world proof this ticket exists for.

No separate live session needed; no new finding beyond what STRESS-010 already surfaced.
