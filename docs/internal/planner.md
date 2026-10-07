# internal/planner

`internal/planner` computes the diff between a project's freshly resolved dependencies and its previously recorded state (version references, active generations), producing a typed list of actions for `ragctl plan`/`ragctl sync` to report or execute. It is the decision layer of sync: it never touches bbolt/Badger/network itself — callers gather the inputs it needs and it does pure diffing.

## Key types and functions

- `ActionKind` — string enum of what `Plan` wants done for one dependency: `ADD_REFERENCE`, `DROP_REFERENCE`, `SYNC_VERSION`, `RETAIN_VERSION`, `GC_CANDIDATE`, `NOOP` (internal/planner/planner.go).
- `Action` — one unit of planned work: `Kind`, `ProjectID`, `Dependency domain.DependencyVersion`, `Reason` (internal/planner/planner.go).
- `GenerationKey(dep)` — deterministic `ecosystem|name|version` key used by callers to build the `activeGenerations` and `noSource` maps before calling `Plan` (internal/planner/planner.go).
- `Plan(ctx, project, resolution, current, reg, activeGenerations, noSource) ([]Action, error)` — pure diff function; for each dependency in `resolution` compares against `current` (`[]domain.VersionReference`) to emit `ADD_REFERENCE`/`DROP_REFERENCE`/`SYNC_VERSION`/`NOOP`, and emits `GC_CANDIDATE` unconditionally for any version a project drops or removes (internal/planner/planner.go).

A version **needs building** when it has no active generation, unless the registry has no manifest for it and sync has already recorded it as having no docs source (`noSource`, PLAN-005). That rule applies even when the project's reference is unchanged: a version whose earlier build failed gets `SYNC_VERSION` (reason "referenced but not built — retrying") on every sync instead of staying unbuilt. Only a version that is built (or known to have no source) with an unchanged reference gets `NOOP`.

`RETAIN_VERSION` is declared in the enum but never emitted: a single project's diff can't prove another project still needs a version. Whether a dropped version is actually deleted is decided fleet-wide by `internal/retention.PlanGC` at GC time.

## Dataflow

```mermaid
flowchart TD
    Store[bbolt Store\nGetResolution / ListProjectReferences /\nGetActiveGeneration / HasNoSource] -->|resolution, current refs,\nactive-gen and no-source bools| CLI[internal/cli: computePlans\n(plan.go, shared by runPlan/runSync)]
    Registry[registry.Registry.Load] -->|*registry.Registry| CLI
    CLI -->|project, resolution, current, reg, activeGenerations| Plan["planner.Plan (pure)"]
    Plan -->|"[]planner.Action"| CLI
    CLI -->|render table/JSON| PlanCmd["ragctl plan"]
    CLI -->|dispatch on Action.Kind| SyncCmd["ragctl sync (sync.go)\naddReference / syncVersion"]
    SyncCmd -->|writes| Store
    SyncCmd -->|SYNC_VERSION drives| Generation[internal/data/generation.Build]
```

Inputs: `internal/cli/plan.go`'s `computePlans` (running inside the daemon) reads a project's `Resolution` and `VersionReference`s from `bbolt.Store`, builds the `activeGenerations` and `noSource` maps via `planner.GenerationKey` + `Store.GetActiveGeneration`/`HasNoSource` (one lookup per distinct dependency version, against the configured `vector.backend`), and loads a `*registry.Registry` — all I/O happens before `Plan` is called.

Outputs: `Plan` returns `[]Action`. `ragctl plan` (plan.go) just renders these. `ragctl sync` (sync.go) switches on `Action.Kind`: `ADD_REFERENCE`/`DROP_REFERENCE` write to the bbolt `references` bucket via `addReference`; `SYNC_VERSION` drives `syncVersion`, which ultimately calls into `internal/data/generation.Build`. `GC_CANDIDATE` actions are provisional markers only — nothing acts on them directly; `internal/retention.PlanGC`, run by `ragctl gc`, decides from every project's references (plus the grace period) which versions are really unreferenced.

## Walkthrough

Project `proj-checkout` (Go, `go.mod`) previously depended on `github.com/aleutian-ai/httpkit@v1.2.0`. A new `go build` resolution bumps it to `v1.3.0`. Trace how `Plan` turns that into actions.

1. **Inputs assembled by the caller.** `internal/cli/plan.go`'s `computePlans` has already loaded, from `bbolt.Store`:
   - `resolution domain.Resolution{Ecosystem: domain.EcosystemGo, ManifestPath: "go.mod", Dependencies: []domain.DependencyVersion{{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "github.com/aleutian-ai/httpkit", Direct: true}, Version: "v1.3.0", ResolvedBy: "go list -m -json all"}}}` (shape from internal/domain/domain.go, 54-59).
   - `current []domain.VersionReference` containing `domain.VersionReference{ProjectID: "proj-checkout", Ecosystem: domain.EcosystemGo, Package: "github.com/aleutian-ai/httpkit", Version: "v1.2.0", Reason: ...}` (internal/domain/domain.go) — the row recorded the last time this project synced.
   - `reg *registry.Registry` (loaded via `registry.Registry.Load`), `activeGenerations map[string]bool` and `noSource map[string]bool`, keyed by `planner.GenerationKey`, telling `Plan` whether `httpkit@v1.3.0` already has a promoted generation and whether it's known to have no docs source. Assume neither: both maps are `false` for `"go|github.com/aleutian-ai/httpkit|v1.3.0"`.

2. **`Plan` indexes `current` by key.** `referenceKey(domain.EcosystemGo, "github.com/aleutian-ai/httpkit")` → `"go|github.com/aleutian-ai/httpkit"`, stored in `currentByKey` (internal/planner/planner.go, 72-74).

3. **Main loop walks `resolution.Dependencies`.** For the one entry (`httpkit@v1.3.0`), `key = "go|github.com/aleutian-ai/httpkit"` is marked `seen[key] = true` (internal/planner/planner.go). `reg.Match(domain.EcosystemGo, "github.com/aleutian-ai/httpkit")` succeeds (a knowledge source is mapped), so `reason` stays `""` (internal/planner/planner.go).

4. **Lookup finds a prior reference with a different version** — `prior.Version` (`"v1.2.0"`) != `dv.Version` (`"v1.3.0"`), taking the `case prior.Version != dv.Version` branch (internal/planner/planner.go). `Plan` builds `oldDep := domain.DependencyVersion{Dependency: dv.Dependency, Version: "v1.2.0"}` and appends three actions in order (internal/planner/planner.go):
   ```go
   Action{Kind: ActionDropReference, ProjectID: "proj-checkout", Dependency: oldDep /* v1.2.0 */, Reason: "version changed"}
   Action{Kind: ActionGCCandidate, ProjectID: "proj-checkout", Dependency: oldDep /* v1.2.0 */, Reason: "no longer referenced by this project (provisional — see RET-001)"}
   Action{Kind: ActionAddReference, ProjectID: "proj-checkout", Dependency: dv /* v1.3.0 */, Reason: ""}
   ```
   Since the new version needs building (no active generation, and the registry has a match), a fourth action is appended (internal/planner/planner.go):
   ```go
   Action{Kind: ActionSyncVersion, ProjectID: "proj-checkout", Dependency: dv /* v1.3.0 */, Reason: ""}
   ```

5. **Dropped-dependency pass finds nothing.** `httpkit` was `seen`, so the second loop over `currentByKey` (internal/planner/planner.go) emits no additional actions for it.

6. **Result:** `Plan` returns `[]Action` with exactly those four entries, in that order: `DROP_REFERENCE(v1.2.0)`, `GC_CANDIDATE(v1.2.0)`, `ADD_REFERENCE(v1.3.0)`, `SYNC_VERSION(v1.3.0)`. `ragctl plan` renders them as-is; `ragctl sync` (sync.go) would write the `DROP_REFERENCE`/`ADD_REFERENCE` rows to the `references` bucket and drive `internal/data/generation.Build` for the `SYNC_VERSION` action, while leaving the `GC_CANDIDATE` marker for `internal/retention.PlanGC` to evaluate later.

## Notes

- `Plan` is deliberately side-effect-free: no bbolt/network access inside it, per PLAN-001's design (internal/planner/planner.go). This is why the function signature carries an already-resolved `*registry.Registry` and precomputed `activeGenerations`/`noSource` maps instead of doing those lookups itself.
- Every version a project drops (version bump or dependency removal) is marked `GC_CANDIDATE` *unconditionally*; whether another project still needs it is checked later by `internal/retention`, not here.
- `NOOP` means "built, and nothing changed" — it does not check that the active generation's stored content is still present. Forcing a rebuild of such a version is `ragctl sync --rebuild --dependency X`, which clears the reference and active pointer before planning (see `docs/internal/cli.md`).
- A version bump emits `DROP_REFERENCE` for the old version *and* `ADD_REFERENCE` for the new one in the same pass (internal/planner/planner.go) — without the re-add, nothing would mark the new version as referenced and RET-001's grace-period logic would eventually reap it as orphaned even though it's exactly what the project now depends on.
- Dropped-but-unseen dependencies are collected into a slice and sorted before emitting actions (internal/planner/planner.go) specifically for deterministic output, since Go map iteration order is not stable.
- A dependency with no matching registry source still produces `ADD_REFERENCE`/`SYNC_VERSION` actions, just flagged via `Action.Reason = "no knowledge source mapped"` (internal/planner/planner.go) rather than being silently skipped.
