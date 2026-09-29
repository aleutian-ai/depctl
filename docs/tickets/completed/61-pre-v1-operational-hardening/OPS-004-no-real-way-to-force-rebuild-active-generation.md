# OPS-004: No real way to force-rebuild an already-active generation

**Epic:** Pre-v1.0 Operational Hardening
**Status:** done — 2026-09-28
**Depends on:** OPS-003 (its fix is what made this reachable/discoverable at all)
**Estimated size:** small–medium

## Problem, confirmed live against real production data (2026-09-28)
`ragctl doctor`'s "empty active generations" check (`checkEmptyActiveGenerations`, `internal/cli/doctor.go`) tells the operator to fix a detected empty generation with:
```
re-sync with `ragctl sync --force`
```
**This does not work.** Traced and reproduced live: `internal/planner/planner.go`'s `Plan` function has three branches per dependency — no prior reference, version changed, or (the `default` case) version unchanged since the last scan. The `default` branch emits `ActionNoop` **unconditionally**, without ever checking whether an active generation actually exists, has real backend content, or anything else. `--force` (`internal/cli/sync.go`) is threaded through to `syncVersion` and only affects whether a *already-selected* `SYNC_VERSION` action bypasses VAL-002 sanity thresholds — it has zero effect on which actions the planner selects in the first place. So for any dependency whose resolved version hasn't changed (the exact, common case doctor's check exists to catch — a generation that looks fine in every recorded field but whose real backend content is gone), `ragctl sync --force` reliably no-ops: confirmed live with `0 synced, 0 failed, 0 skipped`.

This was found and worked around manually this session (a real, ~132-generation incident, see `docs/architecture.md`'s COORD-003/OPS-003 section and this epic's own history): the actual fix required directly clearing the `active_generations` bbolt pointer *and* the project's `VersionReference` record for each affected dependency (via `Store.RemoveReference`, an existing but not CLI-exposed method) before a normal sync would do anything. That's not something an operator following doctor's own advice could reasonably do — it required reading planner internals and writing a throwaway Go program against the real database.

## Non-goals
- Not a redesign of the planner's NOOP-on-unchanged-version behavior — that's correct and intentional for the overwhelmingly common case (nothing needs to change if the version hasn't changed and the generation is actually healthy). This ticket is about the *abnormal* case doctor's own check exists to detect: an active generation whose real backend content is gone despite unchanged metadata.
- Not a general "rebuild everything" bulldozer — the fix should be scoped to a specific, named dependency (or the specific set doctor flagged), never a blanket "ignore all planner state" mode.
- Not a fix to the root cause doctor detects (a wiped/reset vector backend, a promotion/replication race, etc.) — this ticket is purely about giving operators a real, supported command to *recover* once such a state is detected, whatever caused it.

## Design direction (not finalized)
Likely shape: a real `--rebuild` (or similarly-named) flag/mode on `ragctl sync`, scoped to explicitly-named dependencies (mirroring `--dependency`'s existing scoping), that does exactly what this session's manual fix did — clear the active pointer and version reference for that exact (ecosystem, dependency, version) tuple before planning, so the planner's `!hadPrior` branch fires and a genuine fresh `SYNC_VERSION` action gets generated and executed. This reuses `Store.RemoveReference` (already exists, already used by the planner's own `ActionDropReference` path) rather than inventing new store mechanics.

Once this exists, fix `checkEmptyActiveGenerations`'s own remedy text to name the real command instead of the currently-incorrect `ragctl sync --force`.

## Inputs / Outputs
- Input: a dependency (ecosystem, name, version) that doctor has flagged as an empty active generation despite unchanged metadata.
- Output: a real, single-command way to force it through a genuine rebuild, with before/after `doctor` output showing the fix took effect (matching how this was verified manually this session).

## Failure behavior
- The rebuild command must not affect any other dependency's reference/active-pointer state — scoped exactly to what's named, never a wider blast radius.
- If the named dependency isn't actually in a bad state, running this should still be safe (a real, if unnecessary, rebuild) — never require the operator to first prove there's a real problem.

## Tests
- A test asserting the new command/flag actually clears the reference+active pointer and that a subsequent plan computation produces a real `SYNC_VERSION` action (not `NOOP`) for exactly the named dependency, and `NOOP` for everything else in the same project.
- Live-verified against a real database with a deliberately-planted "active but empty" generation (reusing this session's own reproduction shape), confirming `doctor` reports clean after the rebuild.

## Acceptance criteria
- [x] A real, documented, single-command way exists to force-rebuild one already-active dependency+version, without manual database surgery.
- [x] `checkEmptyActiveGenerations`'s remedy text names the real command, not the currently-incorrect `ragctl sync --force`.
- [x] Scoped exactly to the named dependency — verified by a test that other references/active pointers in the same project are untouched.
- [x] Live-verified end to end: planted a real "active but empty" generation (a real sync, then deleted its real Qdrant points directly, leaving bbolt bookkeeping untouched — the exact incident shape), confirmed plain `--force` still no-ops (`0 synced`) as predicted, then confirmed `--rebuild --dependency` fixes it for real (`1 synced`, `doctor` reports clean, real point count restored).

## Implementation notes (2026-09-28)
New `Store.ClearActiveGeneration(ctx, ecosystem, dependencyName, backendName)` (`internal/control/bbolt/active_generations.go`) — the inverse of `PromoteGeneration`'s "supersede whichever was active" half: demotes the generation to `SUPERSEDED` and deletes the `active_generations` pointer, a no-op if nothing was active. New `clearForRebuild` (`internal/cli/sync.go`) combines this with the existing `Store.RemoveReference` for every project (or one, if `--project` given) whose current resolution includes one of the named dependencies — run inside `RunSync` before `computePlans`, so `planner.Plan`'s `!hadPrior` branch fires and a genuine `SYNC_VERSION` action gets planned.

Wired end to end: new `--rebuild` flag on `ragctl sync` (validated: requires `--dependency`, rejected with `--dry-run` since a dry-run plan wouldn't reflect the rebuild's own effect), `api.SyncRequest.Rebuild` → `SyncOptions.Rebuild` → `RunSync`'s new `rebuild` parameter. `RunSync`'s exported signature changed (added `rebuild bool`) — updated all four call sites (`internal/cli/daemon.go` and three existing tests).

Tests: `TestClearActiveGenerationDemotesAndUnpoints`/`TestClearActiveGenerationOnNothingActiveIsNoop` (bbolt-level), `TestClearForRebuildScopedToNamedDependencyOnly` (proves exact scoping — a second, untouched dependency in the same project stays fully active/referenced, and a fresh `planner.Plan` call produces `SYNC_VERSION` for the rebuilt one and `NOOP` for the untouched one), `TestSyncRebuildRequiresDependency`/`TestSyncRebuildRejectsDryRun` (CLI validation). Full native test suite, `go vet`, and `go build ./...` all clean.

`checkEmptyActiveGenerations`'s remedy text now explicitly says `--force` will not work and names `--rebuild --dependency <name>` instead.
