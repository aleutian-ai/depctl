# VAL-004: Atomic promotion

**Epic:** Validation and Promotion
**Status:** planned
**Depends on:** VAL-001, VAL-003, STORE-001
**Estimated size:** medium

## Goal
Promote a validated candidate generation to `ACTIVE` via a single bbolt write transaction that atomically supersedes the prior active generation and swaps the active-generation pointer — with no network/remote calls inside the transaction.

## Non-goals
- No cross-backend transactional guarantees — the vector backend write already happened before this ticket's transaction; this ticket only governs the local bbolt pointer swap.
- No automatic rollback triggering (VAL-004 only handles promotion; rollback-on-demand is a separate CLI command, out of scope here).

## Simplicity constraints
- The entire promotion transaction is one bbolt `Update` call touching exactly: prior generation record (state → `SUPERSEDED`), candidate generation record (state → `ACTIVE`), and the `active_generations/<dependency-id>/<backend-id>` pointer. Nothing else belongs inside this transaction.

## Design
- Package: `internal/lifecycle/promote`.
```go
func Promote(ctx context.Context, store *bbolt.Store, candidate domain.Generation, validation ...StructuralResult) error
```
- Preconditions checked before opening the transaction: all supplied `StructuralResult`s (VAL-001, VAL-002, VAL-003) must have `Passed = true`; candidate must be in state `READY`.
- Transaction body (single `db.Update(func(tx *bolt.Tx) error { ... })`):
  1. Read current active generation for this dependency+backend (if any).
  2. If present, set its state to `SUPERSEDED`.
  3. Set candidate state to `ACTIVE`.
  4. Write `active_generations/<dependency-id>/<backend-id>` = candidate.ID.
  5. Commit.
- No HTTP/Git/embedding calls anywhere in this function body — all of that already happened upstream (GEN-002, EMB-*, VEC-002) before `Promote` is called.

## Inputs / Outputs
- Input: a `READY` candidate generation plus its passed validation results.
- Output: candidate is `ACTIVE`, prior generation (if any) is `SUPERSEDED`.

## Failure behavior
- If any validation result failed, `Promote` returns an error immediately without opening a transaction.
- If the bbolt transaction fails (e.g. crash mid-write), bbolt's own transaction guarantees ensure the old active pointer remains intact (no torn writes) — verified by test, not by ragctl-level logic.

## Tests
- Full promotion flow: candidate `READY` + prior `ACTIVE` → after `Promote`, candidate is `ACTIVE`, prior is `SUPERSEDED`, active pointer updated — all readable after closing and reopening the bbolt file (restart persistence).
- Simulated failure before the transaction commits (e.g. failing validation) leaves the old active pointer untouched.
- Promote is rejected if any validation result has `Passed = false`.

## Acceptance criteria
- [ ] Crash simulation before the transaction commits leaves the prior generation active.
- [ ] A failing candidate is never promoted, verified by test asserting `Promote` returns an error and touches no bbolt state.
