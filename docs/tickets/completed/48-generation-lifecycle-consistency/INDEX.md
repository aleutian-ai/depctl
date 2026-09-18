# Epic: Generation Lifecycle Consistency

Two small, real fixes recommended by VALID-001's post-implementation note (`docs/tickets/completed/45-competitive-validation`) — both found by live-testing the atomic-promotion invariant, neither invented speculatively.

## Tickets
- [FIX-001](FIX-001-replicate-failure-sets-generation-failed.md) — a `Replicate`-stage failure now transitions `Generation.State` to `FAILED`, matching `Build`-stage failures, instead of leaving the record stuck at whatever state `Build` last set (`INDEXING`).
- [FIX-002](FIX-002-search-project-checks-version-promoted.md) — `searchProject` now checks that the project's *specific resolved version* was itself successfully promoted at some point, instead of only checking that *some* generation is active for the dependency+backend. The first implementation attempt was wrong — it broke VALID-002's already-proven multi-project isolation guarantee — caught by the existing test suite before landing; see that ticket's own post-implementation note for the full story.

## Non-goals
- No change to the atomic-promotion invariant itself (VALID-001 already proved it holds) — these are observability/error-quality fixes, not safety fixes.
