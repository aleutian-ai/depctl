# PLAN-003: `depctl sync` command

**Epic:** Planner and Sync
**Status:** done
**Depends on:** PLAN-002, VAL-004
**Estimated size:** medium

## Goal
Execute the actions produced by the planner: for each `SYNC_VERSION` action, run the full generation-builder → embed → replicate → validate → promote pipeline; apply reference add/drop bookkeeping for `ADD_REFERENCE`/`DROP_REFERENCE`.

## Non-goals
- GC execution itself is a separate command (`depctl gc`, RET-004) — `sync` only marks/updates reference state, it does not delete anything.

## Simplicity constraints
- Sequential execution per dependency in v0.1 — no concurrent worker pool. If one dependency's sync fails, continue with the next unless `--force` semantics say otherwise (see below); do not build a job queue/scheduler for this ticket (that's a later milestone if ever needed).

## Design
- Cobra command: `depctl sync [--dry-run] [--project <id>] [--dependency <name>] [--offline] [--force]`.
- Flow:
  1. Run planner (PLAN-001) exactly as `depctl plan` does.
  2. `--dry-run`: print the plan (same as `depctl plan`) and exit without executing.
  3. For each `SYNC_VERSION` action (filtered by `--project`/`--dependency` if given): build generation (GEN-002), embed (EMB-*), replicate (VEC-002), validate (VAL-001/002/003), promote (VAL-004). Sanity-threshold failures (VAL-002) block promotion unless `--force` is set.
  4. For each `ADD_REFERENCE`/`DROP_REFERENCE`: update `VersionReference` records in bbolt (milestone 15's reference accounting, RET-001, is the actual reference-count logic; this ticket calls into it).
  5. `--offline`: skip any action requiring network access (Git fetch, HTTP acquisition, remote embedding/vector backend); those actions are reported as skipped, not failed.
- A no-op plan (all `NOOP` actions) performs zero backend/network writes — this must be explicitly verified, not just assumed.

## Inputs / Outputs
- Input: computed plan (or freshly computed if not piped from `plan`).
- Output: generations built/promoted, reference records updated, a summary report (counts of synced/failed/skipped).

## Failure behavior
- One dependency's sync failure (e.g. Git fetch failure) does not abort the whole `sync` run for independent dependencies — each `SYNC_VERSION` action is isolated, failures are collected and reported at the end, unless a global `--fail-fast`-style config is later added (not in v0.1).
- Exit code reflects overall outcome: 0 if all actions succeeded, non-zero if any failed.

## Tests
- No-change sync (`NOOP`-only plan) → assert zero backend/network writes (use a fake backend/embedder that panics on any call, prove they're never invoked).
- One dependency sync failure does not prevent another independent dependency's sync from succeeding.
- `--dry-run` performs no writes at all.
- `--offline` skips network-requiring actions and reports them distinctly from failures.

## Acceptance criteria
- [x] No-change sync performs zero backend writes (test-verified, not just documented).
- [x] Failed dependency sync does not block independent dependency syncs.

## Post-implementation notes
- **A real bug caught by writing the "no-change sync performs zero backend writes" test literally, not just documenting it**: the first implementation built the embedder/vector-backend/git-cache pipeline (and probed `embedder.Dimensions` — a live HTTP call) unconditionally whenever `--offline` wasn't set, regardless of whether the computed plan actually contained any `SYNC_VERSION` action. A genuinely no-op sync (nothing changed) would still have made a live network call. Fixed by building the pipeline lazily — only on the first `SYNC_VERSION` action that actually needs it, via a `getPipeline()` closure memoizing the result across the run. `TestSyncNoOpPlanMakesNoNetworkCalls` (`internal/cli/sync_test.go`) is the regression test: it seeds a `VersionReference` matching the resolved version exactly (forcing a `NOOP`, not a `SYNC_VERSION`), then runs `sync` *without* `--offline` and asserts it returns within 10s rather than hanging on an unreachable configured endpoint.
- `--force` only overrides a `Sanity` (VAL-002) failure, never `Structural` (VAL-001) or `VersionCorrectness` (VAL-003) — the ticket's "Sanity-threshold failures block promotion unless `--force` is set" is taken literally: structural/version-correctness failures indicate the replica itself is broken, not an implausible-but-legitimate count change, and are never forceable.
- Reference-count/GC logic (RET-001, epic 16) doesn't exist yet, so `ADD_REFERENCE`/`DROP_REFERENCE` execution here is the interim simplification the ticket itself flags ("this ticket calls into it") reduced to direct `PutVersionReference`/`DeleteVersionReference` calls with first-seen/last-seen bookkeeping — no reference counting across projects, no GC eligibility computed from it.
