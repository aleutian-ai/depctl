# FIX-002: searchProject checks the resolved version was itself promoted

**Epic:** Generation Lifecycle Consistency
**Status:** done
**Depends on:** none
**Estimated size:** small (turned out larger — see post-implementation note)

## Goal
Recommended by VALID-001's post-implementation note: `query.Service.searchProject` returned a silently empty `SearchResult` — not an error — for a project resolved to a version with no promoted generation of its own, when some *other* version of the same dependency happened to be the currently-active generation. The safety property held (never cross-version content), but the failure mode an agent actually saw was a confusing empty result instead of an actionable `ErrNoActiveGeneration` — and it meant `search_dependency_docs`'s JIT-sync trigger (WATCH-019, which pattern-matches `ErrNoActiveGeneration`) never fired for this exact case.

## Non-goals
- No change to the actual version-correctness guarantee — `backend.Filter{Version: dep.Version}` already made cross-version content leakage structurally impossible (VALID-001 confirmed this); this ticket is purely about the error signal a caller sees before that filter ever runs.
- No change to `searchLatest`/`searchCompare`/`searchAllRetained` — scoped to `searchProject` only, the mode this finding was about.

## Simplicity constraints
- Reuses `internal/control/bbolt.Store.ListGenerationsByDependencyVersion` (already existed, added for GC-003) rather than inventing a new store method — added it to `query.ControlStore`'s narrow interface.

## Design
`searchProject` gains a second check, after the existing `GetActiveGeneration` existence check: does *any* generation for `(ecosystem, package, dep.Version)` — the project's own specific resolved version — have `State == GenActive` or `State == GenSuperseded` (both mean "this generation was successfully validated and promoted at some point," per `internal/domain/lifecycle.go`'s state machine; `GenReady` is pre-promotion and doesn't count). If not, `ErrNoActiveGeneration`.

## Inputs / Outputs
- Input: a project resolved to a dependency version.
- Output: `ErrNoActiveGeneration` if that specific version was never promoted; otherwise the existing version-filtered search proceeds unchanged.

## Failure behavior
`ListGenerationsByDependencyVersion` erroring is treated the same as `GetActiveGeneration` erroring — `ErrNoActiveGeneration`, not a raw store error, matching this function's existing error-shape convention.

## Tests
- `TestSearchProjectModeVersionMismatchReturnsTypedErrorNotEmptyResult` (`internal/query/search_test.go`) — the regression test: a project resolved to a version that was never seeded/promoted at all returns `ErrNoActiveGeneration`, not an empty success.
- Existing `TestModeProjectStructurallyExcludesOtherProjectsVersion`/`TestModeCompareAndAllRetainedBehaveAsDocumented` (VALID-002, `internal/cli/cross_project_isolation_test.go`) — the control case: Project A resolving v1.0.0 after Project B's v2.0.0 promotion supersedes it as the active pointer must still succeed, since v1.0.0 *was* promoted at its own time.

## Acceptance criteria
- [x] A project resolved to a version that was never promoted gets `ErrNoActiveGeneration`, not an empty successful result.
- [x] A project resolved to a version that *was* promoted, but is no longer the current active pointer (superseded by a different project's later promotion of a different version), still succeeds — VALID-002's scenario is unaffected.
- [x] `go test ./...` passes, including `internal/query`, `internal/mcp`, and `internal/cli`.

## Post-implementation note

**The first implementation attempt was wrong, and the existing test suite caught it before it shipped** — exactly the discipline this whole ticket-and-testing approach exists to enforce, working as intended.

The naive fix compared `GetActiveGeneration`'s returned generation's `Version` directly against `dep.Version` — plausible-sounding, and it made `TestSearchProjectModeNoActiveGenerationReturnsTypedError`-style cases pass. But running the full suite immediately broke `TestModeProjectStructurallyExcludesOtherProjectsVersion` and `TestModeCompareAndAllRetainedBehaveAsDocumented` (VALID-002) — because `active_generations` is a **single, backend-scoped pointer per (ecosystem, package, backend)**, not per-project or per-version. Project A resolving v1.0.0, after Project B's later v2.0.0 promotion supersedes v1.0.0 as *the* active pointer, is exactly the case where the active pointer's version legitimately differs from a project's own resolved version — yet the search must still succeed, since v1.0.0's real, synced content is completely untouched by v2.0.0's promotion. The naive check couldn't distinguish that from VALID-001's actual case (a version that was *never* promoted at all) — both look identical as "active generation's version ≠ my resolved version."

Reverted, then re-fixed correctly: instead of comparing against the *current* active pointer, the fix checks whether the project's *specific resolved version* was **ever itself** promoted (`ListGenerationsByDependencyVersion` + `State ∈ {ACTIVE, SUPERSEDED}`), independent of whether it's still the current pointer. This distinguishes the two cases correctly. Required adding `ListGenerationsByDependencyVersion` to `query.ControlStore`'s interface and both packages' `fakeControlStore` test doubles (`internal/query`, `internal/mcp`) — their shared `seedChunk` helper now also records each seeded generation into a `generations` history list (State `ACTIVE`), not just the single active-pointer map entry, so a test seeding two versions of the same dependency (VALID-002's exact shape) correctly represents "both were promoted, only one is currently the pointer" rather than losing the first one's promotion history entirely.

Re-verified against both scenarios directly: `TestSearchProjectModeVersionMismatchReturnsTypedErrorNotEmptyResult` (new, this ticket) and VALID-002's own real-bbolt-backed tests (`internal/cli/cross_project_isolation_test.go`) both pass. Full repo build/vet/gofmt/test clean.
