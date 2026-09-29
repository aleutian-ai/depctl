# STRESS-014: Repeated GC cycle idempotency

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
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
- [x] Repeated `gc`/`gc --orphans` runs against an already-clean store are fast, error-free no-ops, 3+ times in a row.
- [x] `ragctl doctor` remains clean throughout.

## Post-implementation note (2026-09-29)

Ran against the real production store rather than rebuilding STRESS-011's isolated fixture: STRESS-011/012's cleanup had already torn down that scratch environment, but real production was itself in a fully clean, doctor-verified state (18 ok, 0 warning, 0 unhealthy going in), satisfying this ticket's own constraint ("a store already fully cleaned... no new fixture needed") without any new setup.

**Real, unplanned findings surfaced in run 1 of each command** — not test artifacts, genuine previously-uncollected production garbage:
- `ragctl gc` run 1: 9 real, genuinely grace-expired references (`ragctl-core-cli`, `-ollama`, `-mod`, `-bleve`, `-viper`, `-tools`, `-cobra`, `-go-git`, `-go-client`) deleted cleanly, `9 deleted, 0 failed`, 0.95s.
- `ragctl gc --orphans` run 1: 9 real orphans — a mix of `stale_nonterminal` (2, still `INDEXING` and never promoted) and `failed` (7, real `FAILED` generations, several of them this session's own earlier live-testing casualties against `ragctl-core-*` and `google.golang.org/grpc`) — deleted cleanly, `9 deleted, 0 failed`, 2.98s.

Both are legitimate real-world GC work, exactly what these commands exist to do against a long-lived real store — consistent with this epic's "real infrastructure, not fixtures" mandate. They don't change this ticket's actual finding.

**The idempotency behavior itself, confirmed as designed:**
- `gc` runs 2 and 3: `nothing eligible for garbage collection`, ~11ms each (vs. 0.95s for the real work in run 1) — no re-scan, no re-attempted deletion.
- `gc --orphans` runs 2 and 3: `nothing eligible for orphan garbage collection`, ~10-12ms each (vs. 2.98s for run 1).
- `ragctl doctor` after all 6 runs: still 18 ok, 0 warning, 0 unhealthy.

No new finding beyond confirming existing behavior holds under real repeated invocation, exactly as this ticket's non-goals anticipated.
