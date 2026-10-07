# internal/retention

`internal/retention` implements ragctl's grace-period bookkeeping (RET-002) and GC eligibility planning (RET-003): keeping a dependency version's knowledge retained for a configurable window after a project stops referencing it, then computing which versions are finally safe to delete. It also plans two narrower cleanups: orphan generations (failed or stuck builds) and superseded same-version duplicates. It is pure over store reads — no deletion happens in this package; that's `internal/lifecycle/gc`.

## Key types and functions

- `ControlStore` — narrow consumer-side interface over `*bbolt.Store`: `AddReference`, `RemoveReference`, `ListReferences`, `ListAllReferences`, `GetActiveGeneration`, `ListAllGenerations` (internal/retention/retention.go).
- `DropReference(ctx, store, ecosystem, pkg, version, projectID)` — removes `projectID`'s reference; if no reference of any reason remains afterward, automatically adds a `grace_period` reference so the version doesn't become immediately GC-eligible. A version still held by another project or a `latest`/`manual_pin` reference is left alone (internal/retention/retention.go).
- `EffectiveGracePeriod(configured)` — returns `configured`, or the documented 14-day (336h) default if `configured <= 0` — a zero/missing config must never mean "no grace period" (internal/retention/retention.go).
- `GraceExpiry(r, gracePeriod)` — `r.LastSeenAt.Add(gracePeriod)`; only meaningful for a `grace_period`-reason reference (internal/retention/retention.go).
- `GCCandidate` — one `(Ecosystem, Package, Version, Reason)` tuple RET-003 has determined is safe to delete (internal/retention/gc_planner.go).
- `PlanGC(ctx, store, backendName, gracePeriod, now)` — computes every dependency version eligible for GC: no `project`/`latest`/`manual_pin` reference blocking it, and a `grace_period` reference exists and is expired. Being active does **not** protect a version (ADR-012: each version stays active until GC retires it, so excluding active versions would make every unreferenced version immortal); `gc.Run` clears the active pointer as its first step. `backendName` is currently unused (internal/retention/gc_planner.go).
- `OrphanCandidate` — one `domain.Generation` (identified by `GenerationID`, not dependency+version) GC-001 has determined is safe to delete: `FAILED`, or stuck non-terminal past `orphanAge` and not currently active (internal/retention/orphan_planner.go).
- `EffectiveOrphanAge(configured)` — same zero/missing-config-must-not-mean-immediate defaulting pattern as `EffectiveGracePeriod`, 24h default (internal/retention/orphan_planner.go).
- `PlanOrphanGC(ctx, store, backendName, orphanAge, now)` — a full `ListAllGenerations` scan for `FAILED` generations, or ones stuck in `ACQUIRING`/`NORMALIZING`/`INDEXING`/`VALIDATING` and untouched for longer than `orphanAge`, skipping any that is still its version's active generation. A full scan (an orphan, by definition never promoted, has no dependency+version reference index pointing at it the way `PlanGC`'s candidate discovery does) — an entirely separate eligibility path from `PlanGC`, never merged with it (epic 22, internal/retention/orphan_planner.go).
- `SupersededDuplicateCandidate`, `PlanSupersededDuplicateGC(ctx, store)` — every `SUPERSEDED` generation whose exact (ecosystem, package, version) also has an `ACTIVE` generation: the leftover of two builds of the same version, only one promoted. Its content duplicates what's being served, so it needs no grace period or reference check (internal/retention/superseded_duplicate_planner.go).

## Dataflow

```mermaid
flowchart TD
    Sync["ragctl sync\n(cli/sync.go, on planner.ActionDropReference)"] -->|DropReference| DR["retention.DropReference"]
    DR -->|RemoveReference, then\nAddReference(grace_period) if none remain| Bbolt1[(bbolt: references bucket)]

    GCCmd["ragctl gc (cli/gc.go)"] -->|PlanGC| Plan["retention.PlanGC"]
    Bbolt2[(bbolt: references bucket)] -->|ListAllReferences, ListReferences| Plan
    Plan -->|"[]GCCandidate"| GCRun["lifecycle/gc.Run"]
    GCRun -->|clears active pointer; deletes index +\nBadger + bbolt data, dependency+version scoped| Stores[(search indexes, Badger, bbolt)]

    OrphanCmd["ragctl gc --orphans\n(cli/gc.go, GC-002)"] -->|PlanOrphanGC| OPlan["retention.PlanOrphanGC"]
    Bbolt3[(bbolt: generations bucket,\nactive_generations)] -->|ListAllGenerations,\nGetActiveGeneration| OPlan
    OPlan -->|"[]OrphanCandidate"| GCRunOrphans["lifecycle/gc.RunOrphans"]
    GCRunOrphans -->|deletes index + Badger + bbolt data,\nGENERATION-ID scoped only| Stores

    DupCmd["ragctl gc --superseded-duplicates\n(cli/gc.go)"] -->|PlanSupersededDuplicateGC| DPlan["retention.PlanSupersededDuplicateGC"]
    Bbolt3 -->|ListAllGenerations| DPlan
    DPlan -->|"[]SupersededDuplicateCandidate"| GCRunDup["lifecycle/gc.RunSupersededDuplicates"]
    GCRunDup -->|GENERATION-ID scoped only| Stores
```

`DropReference` is called from `ragctl sync`'s action dispatch whenever `planner.Plan` emits `ActionDropReference` — it's the only writer in this package, touching the `references` bbolt bucket. `PlanGC` is called from `ragctl gc` (cli/gc.go); it only reads (`ListAllReferences`, `ListReferences`) and returns `[]GCCandidate` for `internal/lifecycle/gc.Run` to act on — `retention` itself never deletes anything. `PlanOrphanGC` (GC-001) and `PlanSupersededDuplicateGC` (POINT-004) are `ragctl gc --orphans`' and `ragctl gc --superseded-duplicates`' own, independent read paths — no writer of its own in this package, since a generation's `FAILED`/stuck state is already written by `internal/data/generation.Build`/`Replicate`, not by anything in `retention`.

## Walkthrough

Scenario: project `proj-checkout` was the last project referencing `go/github.com/example/widget@v1.2.0`. It re-resolves to `v1.3.0`, so `ragctl sync` drops the old reference — starting a grace period — and, 15 days later, `ragctl gc` evaluates that version for deletion.

1. **`ragctl sync` emits the drop.** `planner.Plan` (see `docs/internal/planner.md`) compares the new `Resolution` against stored `VersionReference`s, sees `proj-checkout` no longer resolves to `v1.2.0`, and emits `ActionDropReference{Ecosystem: "go", Package: "github.com/example/widget", Version: "v1.2.0", ProjectID: "proj-checkout"}`. The CLI's action dispatch calls `retention.DropReference(ctx, store, "go", "github.com/example/widget", "v1.2.0", "proj-checkout")` (internal/retention/retention.go).

2. **The project's reference is removed.** `store.RemoveReference` deletes the `VersionReference{ProjectID: "proj-checkout", Reason: domain.ReferenceReasonProject, ...}` row from the `references` bucket (internal/retention/retention.go).

3. **`DropReference` checks what's left.** `store.ListReferences(ctx, "go", "github.com/example/widget", "v1.2.0")` returns `[]domain.VersionReference{}` — no other project references this version, and there's no `latest` or `manual_pin` row either. Since `len(remaining) == 0`, `DropReference` falls through instead of returning early (internal/retention/retention.go).

4. **A grace-period reference is added.** With `now := time.Now()` resolving to `2026-08-26T09:00:00Z`, `DropReference` calls `store.AddReference` with:
   ```go
   domain.VersionReference{
       ProjectID:   domain.GracePeriodProjectID,
       Ecosystem:   "go",
       Package:     "github.com/example/widget",
       Version:     "v1.2.0",
       Reason:      domain.ReferenceReasonGracePeriod,
       FirstSeenAt: time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC),
       LastSeenAt:  time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC),
   }
   ```
   No duration is stored yet — only `LastSeenAt` (internal/retention/retention.go).

5. **15 days pass; `ragctl gc` runs `PlanGC`.** Called as `PlanGC(ctx, store, "embedded", 0, time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC))`. Say the configured `gracePeriod` is `0` (an older config without the key; fresh installs write 14 days explicitly); `EffectiveGracePeriod` then returns the RET-002 default of `336 * time.Hour` (14 days) (internal/retention/gc_planner.go, internal/retention/retention.go).

6. **The candidate set is discovered.** `store.ListAllReferences(ctx)` returns every reference row across all projects, including the grace-period row from step 4. `PlanGC` dedupes into `dependencyVersion{ecosystem: "go", pkg: "github.com/example/widget", version: "v1.2.0"}` keyed as `"go|github.com/example/widget|v1.2.0"` (internal/retention/gc_planner.go).

7. **Eligibility rules run for that version.** `store.ListReferences` returns just the one `grace_period` row. The loop finds no `project`/`latest`/`manual_pin` reason (so `blocked` stays `false`) and sets `graceRef` to that row (internal/retention/gc_planner.go).

8. **Expiry is checked.** `GraceExpiry(*graceRef, 336*time.Hour)` computes `2026-08-26T09:00:00Z + 14d = 2026-09-09T09:00:00Z`. `now` (`2026-09-10T09:00:00Z`) is after that, so the version passes the expiry check (internal/retention/gc_planner.go, internal/retention/retention.go).

9. **No active-generation guard.** `v1.2.0` is probably still the active generation for its own version — active generations are per version (ADR-012), so `v1.3.0`'s promotion didn't retire it. That doesn't matter: references and the grace period alone decide a version's lifetime, and `gc.Run` will clear the active pointer before deleting anything (internal/retention/gc_planner.go).

10. **The candidate is emitted.** `PlanGC` appends `GCCandidate{Ecosystem: "go", Package: "github.com/example/widget", Version: "v1.2.0", Reason: "grace_expired"}` to its result slice, which `ragctl gc` hands to `lifecycle/gc.Run` to actually delete (internal/retention/gc_planner.go).

## Notes

- `DropReference` intentionally does not take a grace-period duration — only `LastSeenAt` is stored on the new `grace_period` reference; the duration is applied later, once, at `PlanGC`'s eligibility-check time via `EffectiveGracePeriod`. This lets grace-period config change between when a grace period starts and when GC actually runs without needing to rewrite stored references (internal/retention/retention.go).
- `PlanGC` discovers its candidate set via `ListAllReferences` rather than iterating a dedicated `dependency_versions` bucket the original design sketch named — that bucket (a fuller STORE-001 relational split) was never built. Every `(ecosystem, package, version)` a project has ever resolved to already has at least one reference row once `planner.Plan`/`sync` records it, so the `references` bucket is the actual source of enumeration (internal/retention/gc_planner.go).
- `PlanGC` still takes `backendName`, which it needed when it skipped the active generation; since ADR-012 it no longer reads it (internal/retention/gc_planner.go).
- A version is GC-eligible only if it has a `grace_period` reference AND no blocking reference of any other reason — a version with zero references entirely (never had one) is *not* eligible; `DropReference` is what adds the grace-period reference exactly once all other references disappear, so this shouldn't occur for versions that ever went through normal sync/drop flow.
