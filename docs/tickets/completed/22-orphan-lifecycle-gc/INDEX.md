# Epic: Orphan Lifecycle GC

Closes a correctness gap the existing retention/GC epic (`docs/tickets/completed/16-retention-gc`, `internal/retention`, `internal/lifecycle/gc`) never covered: **generations that never became active in the first place**. `retention.PlanGC` and `gc.Run` handle no-longer-referenced *successful* generations — a version that was promoted, served for a while, and is now safe to retire. They say nothing about a generation that failed mid-build, or crashed and got stuck `ACQUIRING`/`NORMALIZING`/`INDEXING`/`VALIDATING` forever. Those can leak Badger content and vector-backend points indefinitely, with no reference/grace-period bookkeeping ever pointing at them (a failed or interrupted generation was, by definition, never promoted, so it has no `dependency+version+active` semantics the existing GC path relies on).

Implements `docs/scratch/depctl_architecture_eval_next_steps-2.md` §11 ("Orphan cleanup: correctness work to do soon") and Phase 2 of that document's recommended sequence.

**Not to be confused with** the `RET` prefix from epic 16 — that's a different, already-shipped mechanism (reference-counted grace-period expiry for *successful* generations). This epic's `GC` prefix is new and additive: it adds a second, independent eligibility path for a different failure class, reusing epic 16's deletion machinery (`internal/lifecycle/gc.Run`'s three-store deletion order) wherever the shapes line up, and diverging only where an orphan's lack of promotion/reference history makes the existing dependency+version-scoped semantics actively wrong (see GC-003).

## Tickets
- [GC-001](GC-001-orphan-generation-planner.md) — **done.** `retention.PlanOrphanGC`, a sibling to `retention.PlanGC` that finds `FAILED` generations, or generations stuck non-terminal past a configurable age, that aren't anyone's active generation.
- [GC-002](GC-002-orphan-gc-cli-dry-run.md) — **done.** `config.Retention.OrphanAge` and `depctl gc --orphans [--dry-run]`, opt-in and separate from the existing `depctl gc` reference-based path. Implemented against the real daemon-client architecture (`api.GCRequest.Orphans`, `Scheduler.RequestOrphanGC`) rather than the ticket's pre-daemon-refactor sketch — see its own post-implementation note.
- [GC-003](GC-003-generation-scoped-backend-deletion.md) — **done.** The actual deletion path for an orphan candidate: reusing `backend.Filter`'s existing (already fully wired, previously unexploited for this purpose) `Generation` field for vector-backend deletion, and `internal/lifecycle/gc`'s existing per-generation Badger/bbolt deletion pattern — explicitly *not* reusing epic 16's dependency+version-scoped reference cleanup, which would be unsafe for a generation that was never promoted.

All three shipped in one pass (2026-09), pulled forward as the second-highest-priority gap a design review flagged (after VALID-001). Full repo build/vet/gofmt/test clean throughout.

## Non-goals for this epic
- No lease/heartbeat mechanism for in-progress builds — see GC-001's non-goals for why the existing `Generation.UpdatedAt` plus a configurable age threshold is sufficient without inventing a concept this codebase doesn't have.
- No automatic/implicit orphan cleanup — orphan GC is opt-in via `--orphans` until it's been run safely for a while in practice, per the design doc's explicit safety note (§11.2).
- No change to `retention.PlanGC`, `gc.Run`, or any epic-16 behavior for successful/promoted generations — this epic is purely additive.
