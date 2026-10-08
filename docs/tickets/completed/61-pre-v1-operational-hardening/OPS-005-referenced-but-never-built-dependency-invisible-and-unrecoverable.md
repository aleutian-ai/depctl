# OPS-005: A referenced-but-never-built dependency is invisible to `doctor` and unrecoverable by plain `sync`

**Epic:** Pre-v1.0 Operational Hardening
**Status:** done — 2026-09-28
**Depends on:** OPS-004 (its `--rebuild` flag is the only existing recovery path)
**Estimated size:** small–medium

## Problem, confirmed live (STRESS-006, epic 49, 2026-09-28)
Live-verified via `kill -9`-mid-sync testing against a real daemon: a kill landing after a new dependency's `ActionAddReference` commits but before its matching `ActionSyncVersion` reaches `ACTIVE` (anywhere from early `ACQUIRING` through `INDEXING`) leaves that dependency **permanently un-retriable by any plain `depctl sync`, with no error, warning, or `doctor` finding ever surfacing it.**

Traced precisely: `internal/planner/planner.go`'s `Plan` function has three branches per dependency (see OPS-004's own ticket for the first sibling of this bug). Its `default:` branch — the dependency's resolved version is unchanged since the last scan — emits `ActionNoop` **unconditionally**, without checking whether an active generation for it has *ever* existed. Once the reference is recorded (which happens as its own committed action, separate from and before the matching sync action), every future `depctl sync` sees `hadPrior == true` and a matching version, and silently reports "up to date" — forever, even though the dependency has zero real content and was never successfully built even once.

This is the sibling of OPS-004's "active generation, real content gone" case, but strictly worse: OPS-004's case is at least detectable — `depctl doctor`'s `checkEmptyActiveGenerations` explicitly checks every *active* generation's real backend point count. This case has **no active generation at all**, so that check (and every other existing `doctor` check) has nothing to inspect — the dependency simply doesn't appear in any unhealthy list, ever. `depctl describe` doesn't help either: live-confirmed it only reports the currently-*active* generation per dependency, so a dependency stuck in `ACQUIRING`/`INDEXING` with no active generation shows up in `describe`'s report as if it doesn't exist at all, not as a problem.

The only confirmed recovery path today is `depctl sync --rebuild --dependency <name>` (OPS-004) — which requires an operator to already suspect this exact dependency by name. Nothing surfaces the suspicion in the first place.

## Non-goals
- No change to `depctl gc --orphans`'s existing age-gated eligibility (`RetentionConfig.OrphanAge`, 24h default) — confirmed correct and working as designed during STRESS-006 (a generation stuck for only minutes correctly wasn't yet eligible; that's intentional conservatism, not this bug).
- No change to the planner's `default:`-branch NOOP behavior for the *normal*, overwhelmingly common case (version unchanged, generation genuinely healthy) — that's correct and must stay fast/cheap. This ticket is about *detecting* the abnormal case, not changing the common one.
- Not a fix for `depctl describe`'s own display scope (only showing active generations) in general — worth a note, but the real gap is `doctor`'s missing check, not `describe`'s reporting choice.

## Design direction (not finalized)
Likely shape: a new `depctl doctor` check — call it `checkReferencedButNeverBuilt` or similar — that cross-references every project's recorded `VersionReference`s against `GetActiveGeneration`: any reference with **no** active generation at all (not "empty," genuinely absent) and old enough to no longer plausibly be an in-flight first sync (some short grace window, to avoid false-flagging a sync that's simply still running) gets reported, by name, so an operator knows exactly what to `--rebuild`.

Separately worth considering: should the very first sync attempt for a *newly-added* reference be retried automatically on the *next* `depctl sync` invocation, rather than requiring `--rebuild` forever? That would mean the planner's `default:` branch needs to distinguish "version unchanged, already active" (stay NOOP) from "version unchanged, reference exists, but was *never* active" (should get exactly one more real attempt, matching the `!hadPrior` branch's own behavior) — a real planner change, not just a `doctor` check. Worth weighing against the added complexity; the `doctor`-check-only option is smaller and non-invasive to already-proven planner logic.

## Inputs / Outputs
- Input: a project with at least one dependency whose reference was recorded but which has never reached an active generation (reproducible via STRESS-006's own kill-timing method, or by any other interruption between `ActionAddReference` and a successful `ActionSyncVersion`).
- Output: `depctl doctor` names it explicitly, with the actionable `--rebuild` command to fix it — never silent.

## Failure behavior
- A check that never fires (false negative on the real bug) is worse than one that's occasionally over-eager (false positive on a sync that's still genuinely, slowly, in-flight) — bias the grace window toward catching the real bug, and word the false-positive case's message to account for "might just still be running."

## Tests
- A test planting the exact bbolt state STRESS-006 found live (a `VersionReference` with no corresponding active generation) and confirming the new `doctor` check reports it, naming the dependency and the `--rebuild` remedy.
- A test confirming the check does *not* fire for a genuinely still-in-flight sync (age below the grace window) or a normal, healthy, already-active dependency.
- Live re-verification: reproduce STRESS-006's own kill-timing scenario fresh, confirm `doctor` now reports the stuck dependency by name.

## Acceptance criteria
- [x] A new `depctl doctor` check detects a referenced-but-never-actively-built dependency and names it explicitly, with the `--rebuild` remedy.
- [x] The check doesn't false-positive on a dependency that's still genuinely, legitimately mid-first-sync.
- [x] Live-verified: STRESS-006's own kill-mid-sync scenario reproduced fresh, confirmed now detected by `doctor` where it previously wasn't, and confirmed the suggested remedy actually fixes it.

## Implementation notes (2026-09-28)
Went with the smaller, non-invasive option from the design direction: a new `checkReferencedButNeverBuilt` `doctor` check (`internal/cli/doctor.go`), not a planner change — cross-references every recorded `VersionReference` (`Store.ListAllReferences`) against `GetActiveGeneration`; anything with no active generation at all, older than a grace window (`referencedButNeverBuiltGrace`, 5 minutes — long enough that a real first sync in progress is never false-flagged), gets named explicitly with the `--rebuild --dependency <name>` remedy.

**Live-verified end to end, reproducing STRESS-006's exact scenario fresh** (isolated Podman Qdrant container, real Ollama, a real `kill -9` mid-sync of `golang.org/x/tools`): `doctor` correctly stayed quiet immediately after the kill (still within the grace window — "0 checked, none stuck"), then, after back-dating the reference's `FirstSeenAt` to simulate the grace window elapsing (rather than a real 5-minute wait), correctly reported `UNHEALTHY referenced but never built ... go golang.org/x/tools@v0.21.0` with the exact `--rebuild` command — running that command fixed it for real, final `doctor`: 19 ok / 0 warning / 0 unhealthy.

Tests: `TestDoctorFlagsReferencedButNeverBuilt` (reproduces the exact real bbolt state found live), `TestDoctorReferencedButNeverBuiltSkipsRecentReferences` (no false positive on a fresh reference). Full native test suite, `go vet`, `go build ./...` all clean.
