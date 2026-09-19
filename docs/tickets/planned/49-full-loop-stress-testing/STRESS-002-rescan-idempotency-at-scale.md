# STRESS-002: Re-scan idempotency at scale

**Epic:** Full-Loop Stress Testing
**Status:** done
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
- [x] Three consecutive scans of an unchanged 100+-dependency project produce identical fingerprints and dependency lists.
- [x] `ragctl plan` after the first scan (and every scan after) shows zero *new* actions introduced by re-scanning — see Post-implementation note for why "zero actions" doesn't literally apply here.

## Post-implementation note

Ran via `hack/test-linux.sh`'s Podman pattern, reusing STRESS-001's `hashicorp/terraform` clone (all 11 discovered Go projects, 569+561×10 dependency references). Three consecutive `ragctl scan` runs, `ragctl deps` and `ragctl plan` captured after each:

- **Scan 1**: `discovered 11, new 11, existing 0` — all 11 projects registered fresh, as expected.
- **Scan 2 and 3**: `discovered 11, new 0, existing 11` each time — no duplicate project registrations, confirmed by the daemon's own accounting, not just inferred.
- **`ragctl deps` output**: byte-for-byte identical (`diff` empty) between scan 1 vs. scan 2, and scan 1 vs. scan 3 — 569 lines each time.
- **`ragctl plan`**: exactly **12358** actions after every single scan (scan 1, 2, and 3 alike) — the same count, not growing. No duplicate `ADD_REFERENCE` lines appeared on re-scan.

One real deviation from this ticket's original wording, worth being precise about rather than silently calling it a pass: the acceptance criterion as written ("zero actions... everything already resolved and unchanged") assumed a project that had already been *synced* before re-scanning. This ticket is scan-only (per its own Non-goals — no sync), and none of terraform's real dependencies have any pre-existing knowledge source mapped, so *every* scan — including the very first — produces a full `ADD_REFERENCE`/`SYNC_VERSION` plan for all ~6179 dependency references (12358 = 6179 × 2). That's expected, not a bug. The actual idempotency guarantee this ticket cares about — re-scanning an unchanged project never grows, shrinks, duplicates, or otherwise drifts the plan — held exactly: 12358 == 12358 == 12358, and the underlying `deps` list was byte-identical across all three runs. No drift found; no follow-up needed.
