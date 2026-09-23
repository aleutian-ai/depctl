# VALID-001: Atomic promotion under injected mid-build failure

**Epic:** Competitive Validation
**Status:** done
**Depends on:** existing `internal/data/generation.Build`, `internal/lifecycle/validate.Run`, `internal/lifecycle/promote.Promote`
**Estimated size:** small

## Goal
Prove, with an injected failure rather than an unusually well-behaved test run, the specific invariant the Grounded Docs comparison singled out as the meaningful test: when Project A upgrades `grpc-go` from `v1.71.0` to `v1.75.0` and acquisition/normalization/replication fails partway through, ragctl must never silently serve `v1.71.0`'s content as if it answers a `v1.75.0` query, and must never serve a partial `v1.75.0` replica either. The generation must end up `FAILED`, the prior active generation must remain untouched and still the one served, and `SearchKnowledge` scoped to the new version must report `ErrNoActiveGeneration` rather than silently falling back.

## Non-goals
- No new recovery/retry mechanism — a failed generation is retried by a normal future `sync`, exactly as today; this ticket only proves the failure path is safe, not that it self-heals faster.
- No changes to `promote.Promote`'s transaction itself — this is a test-writing ticket against existing code, not a design change. If the test finds a real gap, that becomes its own follow-up ticket, not silently patched here.

## Simplicity constraints
- Lives in `internal/cli` (or `internal/data/generation`, whichever already has the closest-fitting integration test harness) as a new test function, not a new package.
- Reuses the existing fake `VectorBackend`/embedder test doubles already used elsewhere to fail deterministically after N calls, rather than building new fault-injection infrastructure.

## Design
Test scenario:
1. Sync `grpc-go@v1.71.0` for a project to a real, promoted, `READY` active generation (existing harness pattern, e.g. `scanDepFixture`-style).
2. Bump the project's resolution to `v1.75.0`.
3. Configure the embedder or vector backend test double to fail on (e.g.) the third `Embed`/`Upsert` call — deep enough into `Replicate` that some points have already been upserted for the candidate generation, shallow enough to guarantee `validate.Run` never reaches `VersionCorrectness`.
4. Run `RunSync` (or `cli.syncVersion` directly) for the `v1.75.0` action.
5. Assert:
   - The candidate generation's `bbolt` record is `FAILED`, not `READY`.
   - `active_generations` for this dependency still points at the `v1.71.0` generation — `promote.Promote` was never called for the candidate.
   - `query.Service.SearchKnowledge` with `Mode: ModeProject` still returns `v1.71.0`'s chunks (the project's active generation, correctly reflecting its *old* resolved state until the new one is ready) — never a `v1.75.0`-labeled chunk that's actually partial content.
   - A direct query scoped to `Version: "v1.75.0"` (`ModeAllRetained` or a hypothetical explicit-version query) returns `ErrNoActiveGeneration` or an empty result — never partial `v1.75.0` points, even though some were upserted to the vector backend before the injected failure.
   - The vector backend's orphaned partial-generation points are either absent (if `Replicate`/`Build` cleans up on failure) or, if left behind, are provably unreachable by any `query.Service` code path (documented finding either way — this ticket's job is to establish which is currently true, not to assume one).

## Inputs / Outputs
- Input: a fixture project with one dependency, two sequential sync runs, and an injected failure at a specific point in the second run.
- Output: a passing (or, if it fails, a clearly diagnosed) regression test plus a short note in `docs/architecture.md`'s promotion section recording which of the two orphaned-points outcomes above is actually true today.

## Failure behavior
- If the test finds `SearchKnowledge` *does* leak a partial-generation point under some mode, that is the finding — file it as a new, separate bug ticket rather than expanding this one's scope to fix it.

## Tests
- The scenario above, run end-to-end against a real (test-scoped) bbolt/Badger/fake-vector-backend stack, not mocked at the `query.Service` boundary.
- A second variant: the injected failure lands in `Build`'s `NORMALIZING` phase instead of `Replicate`, confirming the same invariants hold regardless of which pipeline stage fails.

## Acceptance criteria
- [x] A failed mid-upgrade sync leaves the prior version's generation active and correctly served.
- [x] No query mode returns content from the failed candidate generation.
- [x] The finding on vector-backend point cleanup-vs-orphan-but-unreachable is documented, with a follow-up ticket filed if orphaned points turn out to be reachable.

## Post-implementation note

Implemented as `TestSyncVersionAtomicPromotionUnderReplicateFailure` and `TestSyncVersionAtomicPromotionUnderBuildFailure` in `internal/cli/sync_atomic_promotion_test.go` — real bbolt/Badger stores, a real fixture git repo (two tags, distinct content), the real `syncVersion` function, only the embedder faked to fail deterministically. **The headline safety property holds: the prior active generation is never displaced by a failed candidate, and no query mode ever returns the failed candidate's content.** But two real findings emerged that don't match this ticket's original literal wording, documented rather than silently papered over — the discipline this whole ticket exists to enforce:

1. **The failed candidate generation's `State` is not always `FAILED`.** `generation.Build`'s own failures call `generation.fail` explicitly, producing `GenFailed` (confirmed by `TestSyncVersionAtomicPromotionUnderBuildFailure`). But `generation.Replicate`'s failures only mark the *`BackendReplica`* record `FAILED` — the `Generation` record itself is left at whatever state `Build` last set it to (`INDEXING`), confirmed by `TestSyncVersionAtomicPromotionUnderReplicateFailure`. Functionally harmless for the safety invariant (an `INDEXING`-state generation is never promoted or served either way), but a real observability/consistency gap: `ragctl status`/`doctor`/anything inspecting `Generation.State` directly would see a stuck `INDEXING` generation, not an obviously-failed one. **Recommend a small follow-up ticket**: have `Replicate`'s failure path also call `generation.fail` on the `Generation` record itself, for consistency with `Build`'s own convention — filed as its own scoped fix, not expanded into this ticket.
2. **`SearchKnowledge` does not return `ErrNoActiveGeneration` for a project resolving an unpromoted version — it returns success with zero chunks.** `resolveProjectDependency`'s `GetActiveGeneration` check only verifies *some* generation is active for the ecosystem+package (true — the prior version still is); it doesn't check that generation matches the project's *resolved* version. The actual safety comes entirely from `backend.Filter{Version: ...}` matching nothing in the vector search, one layer downstream of where the ticket assumed the guard would be. The safety property still holds, but the failure mode an agent actually sees is a confusingly empty search result, not an actionable "this version isn't synced yet" message — worse UX than `ErrNoActiveGeneration` would give, and notably *different* from what `search_dependency_docs`'s JIT-sync branch (WATCH-019) checks for, since that branch specifically pattern-matches `ErrNoActiveGeneration` and would never trigger here. **Worth a follow-up ticket** to make `searchProject` check the active generation's *version* against the project's resolved version explicitly, both for the clearer error message and so JIT-sync's trigger condition actually fires for this case too — not fixed here, per this ticket's own non-goal against expanding scope into a design change.

Both findings are genuinely `internal/data/generation`/`internal/query` scope, not `internal/cli` scope — flagged here since this ticket's tests are what surfaced them, not filed as separate tickets yet (do that before picking either up).
