# STRESS-013: GC and a queued sync under real daemon load

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
Fire a real `ragctl gc` (reference or orphan) and a real `ragctl sync` at the same project at nearly the same instant, and confirm the daemon's global lock (`Scheduler`'s `s.global` mutex — the same one `RequestGC` and `RequestOrphanGC` both use, per `docs/architecture.md`'s "GC and sync must exclude each other" invariant) actually serializes them under real concurrent daemon load, matching the existing scheduler unit tests but with two genuinely separate client processes, not synthetic in-process timing.

## Non-goals
- No new locking logic — verification only.

## Simplicity constraints
- Reuses whatever project/dependencies are already set up from earlier tickets in this epic; no new fixture needed.

## Design
1. Real daemon, a project with both GC-eligible content (from STRESS-011/012) and a pending sync target.
2. Background both `ragctl gc` and `ragctl sync --dependency <target>` as close to simultaneously as the shell allows.
3. Confirm both complete successfully, neither corrupts the other's work, and (via daemon logs or `ragctl daemon status` during the run, if timing allows) confirm they genuinely ran sequentially, not interleaved.
4. Repeat a few times with GC and sync started in the opposite order, to cover both queuing directions.

## Inputs / Outputs
- Input: concurrent real `gc` and `sync` requests against one daemon.
- Output: pass/fail on both completing correctly with no interleaving corruption, across both orderings.

## Failure behavior
- Any interleaving corruption, deadlock, or incorrect result under this real concurrency is this ticket's finding — a release-blocking bug given the explicit "must never interleave" architectural invariant this would violate.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] Concurrent real `gc` and `sync` requests against one daemon both complete correctly, with no interleaving corruption, in both request orderings.
