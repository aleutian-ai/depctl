# VALID-002: Cross-project version isolation fixture

**Epic:** Competitive Validation
**Status:** planned
**Depends on:** existing `query.Service`, `internal/retention`
**Estimated size:** small

## Goal
The Grounded Docs comparison's "Project A vs Project B" scenario (`grpc-go@v1.71.0` vs `grpc-go@v1.75.0`, same dependency, two projects) is currently a design claim, not a standing, reusable test fixture. Build it as one: two registered projects depending on the same package at two different versions, both actively synced, with a test proving a query scoped to Project A can never structurally return a Project B-version chunk — not "doesn't happen to," but cannot, because the filter makes it unreachable regardless of ranking/scoring behavior.

## Non-goals
- No new retention logic — this exercises the existing project-reference-aware retention (`internal/retention`) as a black box, proving its externally observable guarantee, not re-deriving its internals.
- No comparative benchmarking against another system — that's `context-evals` scope (see epic INDEX).

## Simplicity constraints
- One fixture, reusable by VALID-001, EVAL-002, and (later, externally) any `context-evals` harness — not a one-off inline test.
- Lives alongside existing fixture helpers (e.g. `scanDepFixture`-style) in `internal/cli`'s test helpers, generalized to take two project roots instead of one.

## Design
Fixture: two local go.mod-based fixture projects, each with a local `replace` directive pointing `example.com/foo` (or a real small public module, if a live-network fixture is preferred for realism) at two different tagged commits/versions. Both projects registered and synced to `READY` active generations.

Test matrix:
- `SearchKnowledge(ProjectID: A, Mode: ModeProject)` never returns a chunk whose `Generation` belongs to B's active generation, across a range of query texts (including queries semantically close to content unique to B's version, to stress-test that filtering — not just low relevance ranking — is what's excluding it).
- The same for B scoped against A.
- `ModeCompare` (project-active vs latest) and `ModeAllRetained` are exercised too, since those are the two modes most likely to accidentally cross project boundaries if a filter bug existed — `ModeAllRetained` in particular is documented as "ecosystem-wide, not project-scoped" (see `docs/features/query-serving.md`), so this test should confirm it stays dependency-scoped, not project-scoped, exactly as documented, and never silently leaks a specific project's private/local-replace content into another project's results.
- Both projects' generations survive concurrent retention/GC — the reference-counting mechanism must recognize two live `VersionReference`s to the same dependency and not garbage-collect either.

## Inputs / Outputs
- Input: two fixture projects, one shared dependency at two versions.
- Output: a reusable fixture-construction helper plus the isolation test suite above.

## Failure behavior
- Any leak found here is treated as a release-blocking finding, filed as its own bug ticket immediately — this ticket's job is detection, not fixing.

## Tests
- The matrix described in Design, run against a real bbolt/Badger/fake-vector-backend stack.

## Acceptance criteria
- [ ] A reusable two-project, two-version fixture helper exists.
- [ ] Every query mode is proven to structurally exclude the other project's version-mismatched content, not just typically rank it lower.
- [ ] Retention/GC correctly keeps both versions alive while both projects reference them.
