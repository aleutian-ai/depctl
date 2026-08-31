# PLAN-003: `ragctl sync` command

**Epic:** Planner and Sync
**Status:** planned
**Depends on:** PLAN-002, VAL-004
**Estimated size:** medium

## Goal
Execute the actions produced by the planner: for each `SYNC_VERSION` action, run the full generation-builder → embed → replicate → validate → promote pipeline; apply reference add/drop bookkeeping for `ADD_REFERENCE`/`DROP_REFERENCE`.

## Non-goals
- GC execution itself is a separate command (`ragctl gc`, RET-004) — `sync` only marks/updates reference state, it does not delete anything.

## Simplicity constraints
- Sequential execution per dependency in v0.1 — no concurrent worker pool. If one dependency's sync fails, continue with the next unless `--force` semantics say otherwise (see below); do not build a job queue/scheduler for this ticket (that's a later milestone if ever needed).

## Design
- Cobra command: `ragctl sync [--dry-run] [--project <id>] [--dependency <name>] [--offline] [--force]`.
- Flow:
  1. Run planner (PLAN-001) exactly as `ragctl plan` does.
  2. `--dry-run`: print the plan (same as `ragctl plan`) and exit without executing.
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
- [ ] No-change sync performs zero backend writes (test-verified, not just documented).
- [ ] Failed dependency sync does not block independent dependency syncs.
