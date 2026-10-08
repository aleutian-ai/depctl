# PLAN-001: Desired-state model

**Epic:** Planner and Sync
**Status:** done
**Depends on:** GO-004, REG-003
**Estimated size:** medium

## Goal
Compute the diff between current stored state (project resolutions + active knowledge versions) and desired state (registry-mapped sources for currently resolved dependencies), producing a list of typed actions.

## Non-goals
- Does not execute any action (that's PLAN-003/sync).
- Does not compute retention/GC eligibility beyond emitting `GC_CANDIDATE`/`RETAIN_VERSION` markers — full retention logic (grace periods, reference counting) lives in milestone 15 (RET-*).

## Simplicity constraints
- Planning is a pure function: `(current state, desired state) -> []Action`. No side effects, no I/O beyond reading already-resolved data from bbolt. This keeps it trivially unit-testable and reusable by both `depctl plan` and `depctl sync`.
- Six action kinds only, matching the design spec: `ADD_REFERENCE`, `DROP_REFERENCE`, `SYNC_VERSION`, `RETAIN_VERSION`, `GC_CANDIDATE`, `NOOP`. Do not add more without a concrete driving need.

## Design
- Package: `internal/planner`.
```go
type ActionKind string

const (
    ActionAddReference   ActionKind = "ADD_REFERENCE"
    ActionDropReference  ActionKind = "DROP_REFERENCE"
    ActionSyncVersion    ActionKind = "SYNC_VERSION"
    ActionRetainVersion  ActionKind = "RETAIN_VERSION"
    ActionGCCandidate    ActionKind = "GC_CANDIDATE"
    ActionNoop           ActionKind = "NOOP"
)

type Action struct {
    Kind       ActionKind
    ProjectID  string
    Dependency domain.DependencyVersion
    Reason     string
}

func Plan(ctx context.Context, project domain.Project, resolution domain.Resolution, current []domain.VersionReference, registryMatches map[string]registry.Match) ([]Action, error)
```
- Logic:
  - For each dependency in the new `Resolution`: if not previously referenced by this project → `ADD_REFERENCE`; if version changed from a previous reference → `SYNC_VERSION` (new version) + `DROP_REFERENCE` (old version); if unchanged → `NOOP`.
  - For dependencies previously referenced by this project but absent from the new resolution → `DROP_REFERENCE`.
  - If a dependency version has no active generation yet (first time seen) → `SYNC_VERSION`.
  - Versions with an active generation and no remaining references after this plan is applied are surfaced as `GC_CANDIDATE`; versions still referenced elsewhere are `RETAIN_VERSION`.

## Inputs / Outputs
- Input: one project's current resolution, its stored `VersionReference`s, registry matches for each dependency.
- Output: ordered `[]Action`.

## Failure behavior
- A dependency with no registry match still produces `ADD_REFERENCE`/`SYNC_VERSION` actions but flagged (e.g. `Reason: "no knowledge source mapped"`) rather than erroring the whole plan — one unmapped package should not block planning for the rest.

## Tests
- No prior state, one dependency → single `SYNC_VERSION` + `ADD_REFERENCE`.
- Dependency version bump → `SYNC_VERSION` for new version + `DROP_REFERENCE` for old.
- Unchanged resolution → all `NOOP`, verified this produces literally zero actions requiring work.
- Dependency removed from go.mod → `DROP_REFERENCE`.

## Acceptance criteria
- [x] `Plan` is a pure function covered by table-driven unit tests, no bbolt/network access inside it.

## Post-implementation note
Two inputs the sketched signature didn't show turned out necessary to keep `Plan` actually pure:

- `reg *registry.Registry` instead of `registryMatches map[string]registry.Match` — `registry.Match` isn't a type this codebase has; `Registry.Match(eco, pkg) (Manifest, bool)` already is the lookup, and it's a pure in-memory operation once the registry is loaded (loading itself, I/O, happens in the caller, before `Plan` is invoked).
- `activeGenerations map[string]bool` — "if a dependency version has no active generation yet → SYNC_VERSION" requires knowing whether a generation is already promoted, which `Plan`'s own inputs (one project's resolution + references) can't answer; the caller checks `bbolt.Store.GetActiveGeneration` per dependency version and passes the answer in as a map (keyed by the new `GenerationKey` helper), so `Plan` itself never touches storage.

`GC_CANDIDATE`/`RETAIN_VERSION` are also narrower than the design implies, per this ticket's own non-goals: every version this project drops is marked `GC_CANDIDATE` unconditionally (the correct conservative default), and `RETAIN_VERSION` is never actually emitted — a single project's diff structurally can't prove another project still references a version being dropped here; that requires epic 16's cross-project reference counting (RET-001), which doesn't exist yet. `RETAIN_VERSION` stays in the `ActionKind` enum for RET-001 to produce once it has fleet-wide data.
