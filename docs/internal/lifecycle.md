# internal/lifecycle

`internal/lifecycle` has no top-level `.go` files — it is a grouping directory for the three packages that move a generation through its post-build states:

- `validate` — deterministic pre-promotion checks.
- `promote` — the atomic READY → ACTIVE transition.
- `gc` — executing `internal/retention`'s garbage-collection plans.

Together they answer "is this generation good enough to serve searches" and "is this old data safe to delete". They run after `internal/data/generation.Build`/`Replicate` and around `internal/query`.

A **generation** is one build of one dependency version. Since ADR-012 (control schema v2), the **active generation** is tracked per dependency *version* and backend: several versions of a dependency can be active at once, each serving the projects that resolve to it. Promotion only ever supersedes an earlier build of the same version; old versions leave through retention and GC.

## Key types and functions

**internal/lifecycle/validate**
- `StructuralResult` — shared pass/fail-with-reasons shape every check returns (internal/lifecycle/validate/validate.go).
- `Structural(ctx, gen, manifest, replica, badgerStore)` — VAL-001: non-zero source/object/chunk counts, the replica's point count equals the manifest's chunk count, manifest ID equals generation ID, and Badger's actual staged chunk count equals the manifest's claim (internal/lifecycle/validate/validate.go).
- `SanityConfig`, `DefaultSanityConfig()` — three guardrail thresholds: `MinObjectCountRatio` (0.5), `MaxChunkCountRatio` (3.0), `MaxParserErrorRate` (0.05) (internal/lifecycle/validate/sanity.go).
- `Sanity(ctx, candidate, prior, candidateManifest, priorManifest, cfg)` — VAL-002: compares the candidate's object/chunk counts against `prior`, which sync sets to the most recently promoted generation of any *other* version of the dependency; auto-passes when there is none (internal/lifecycle/validate/sanity.go).
- `SampleChunks(chunks)` — deterministically picks up to 5 chunks, sorted by ID, for the version-correctness smoke test (internal/lifecycle/validate/version_correctness.go).
- `VersionCorrectness(ctx, embedder, vb, ns, gen, sampleChunks)` — VAL-003: uses each sampled chunk's own text as a query (embedded first when there is an embedder; sent as text either way, so the keyword index can answer), filtered to `gen.ID`, and asserts every returned point's version/generation equals `gen`'s. An empty result is a failure too — the index is missing content it should have (internal/lifecycle/validate/version_correctness.go).
- `Report` — bundles the three results; `Passed()` and `Failures()` (internal/lifecycle/validate/run.go).
- `Run(ctx, gen, manifest, replica, prior, priorManifest, cfg, embedder, vb, ns, store, badgerStore)` — sets `gen.State` to `VALIDATING`, runs the three checks, then sets `READY` or `FAILED` (with `Report.Failures()` joined into `gen.Error`) (internal/lifecycle/validate/run.go).

**internal/lifecycle/promote**
- `ErrValidationFailed`, `ErrNotReady` — sentinel errors (internal/lifecycle/promote/promote.go).
- `Promote(ctx, store, candidate, backendName, results...)` — VAL-004: checks every supplied `StructuralResult.Passed` and `candidate.State == READY`, then calls `bbolt.Store.PromoteGeneration` — one write transaction, no network calls, since all acquisition/embedding/replication already happened upstream (internal/lifecycle/promote/promote.go).

**internal/lifecycle/gc**
- `ControlStore`, `DataStore` — narrow consumer-side interfaces over `*bbolt.Store`/`*badger.Store` (internal/lifecycle/gc/gc.go).
- `JobID(jobType, dep)` — deterministic `job_<blake3 hex>` ID from job type + dependency version, so re-running GC against the same candidate resumes the same job (internal/lifecycle/gc/gc.go).
- `Result` — per-candidate outcome: `Candidate`, `Succeeded`, `Error` (internal/lifecycle/gc/gc.go).
- `Run(ctx, control, data, vb, ns, plan)` — RET-004 (`depctl gc`): for each `retention.GCCandidate` (an unreferenced dependency version), claims/resumes a job and deletes in fixed order: clear the version's active pointer → index points (filtered by ecosystem/dependency/version) → Badger data per generation ID → bbolt generation records and reference rows (internal/lifecycle/gc/gc.go).
- `RunOrphans(ctx, control, data, vb, ns, plan)`, `OrphanResult` — `depctl gc --orphans`: deletes `FAILED` or stuck generations, scoped by generation ID only (a healthy generation of the same version may exist), never touching reference rows (internal/lifecycle/gc/orphans.go).
- `RunSupersededDuplicates(ctx, control, data, vb, ns, plan)`, `SupersededDuplicateResult` — `depctl gc --superseded-duplicates`: deletes `SUPERSEDED` generations that share their exact version with an `ACTIVE` one (left by a same-version build race), also scoped by generation ID only (internal/lifecycle/gc/superseded_duplicates.go).

In every case `vb` is the CLI's `searchIndex` built by `buildAllIndexes`, so deletes reach the keyword index and the vector store alike (see [internal/backend](backend.md)).

## Dataflow

```mermaid
flowchart TD
    Build["generation.Build / Replicate"] -->|gen, manifest, replica| Run["validate.Run"]
    Run --> Structural["Structural check\n(counts, replica/manifest/Badger agreement)"]
    Run --> Sanity["Sanity check\n(vs latest other active version)"]
    Run --> VC["VersionCorrectness check\n(sample chunks as queries,\nassert metadata)"]
    Structural --> Report["validate.Report"]
    Sanity --> Report
    VC --> Report
    Report -->|Passed/Failures| StoreState["bbolt: gen.State = READY or FAILED"]
    Report -->|StructuralResult...| Promote["promote.Promote"]
    Promote -->|candidate.State == READY?| PromoteGeneration["bbolt.Store.PromoteGeneration\n(candidate -> ACTIVE for its version;\nsupersede an earlier build of it)"]

    RetentionPlan["retention.PlanGC\n(references + grace expiry)"] -->|[]GCCandidate| GCRun["gc.Run"]
    GCRun --> Job["claim/resume job (bbolt)"]
    Job --> Clear["0. control.ClearActiveGeneration"]
    Clear --> DelVec["1. vb.Delete (every index)"]
    DelVec --> DelBadger["2. data.DeleteGeneration (Badger)"]
    DelBadger --> DelBbolt["3. control.DeleteGenerationRecord +\nDeleteAllReferences (bbolt)"]
    DelBbolt --> JobDone["job.State = SUCCEEDED"]
```

`validate.Run` is called after `generation.Build`/`Replicate` produce a candidate generation, manifest, and `domain.BackendReplica`; its three checks feed a `Report` that both persists `gen.State` and is handed to `promote.Promote`. `promote.Promote` is the sole gate into `ACTIVE`. Separately, `retention.PlanGC` reads bbolt reference/grace-period state to compute `[]retention.GCCandidate`, and `gc.Run` performs the deletion. The orphan and superseded-duplicate passes follow the same job pattern with their own planners in `internal/retention`.

## Walkthrough

**Validating and promoting `gen_9f21ac` for `github.com/spf13/cobra@v1.9.1`**

1. `generation.Build`/`Replicate` have produced a candidate `domain.Generation{ID: "gen_9f21ac", State: domain.GenIndexing, Dependency: {Dependency: {Ecosystem: "go", Name: "github.com/spf13/cobra"}, Version: "v1.9.1"}}`, a `generation.Manifest{ID: "gen_9f21ac", Sources: [...1 source...], ObjectCount: 42, ChunkCount: 311}`, and a `domain.BackendReplica{PointCount: 311}`. The most recently promoted generation of another cobra version is `gen_7a10bd` for `v1.8.0` (`ObjectCount: 40, ChunkCount: 298`); sync passes it as `prior`.

2. `validate.Run` (internal/lifecycle/validate/run.go) sets `gen.State = domain.GenValidating`, persists it via `store.PutGeneration`, then runs the three checks in sequence:
   - `Structural`: 311 == 311 == 311 across manifest, replica, and Badger's own `ListGenerationChunks` count, and `manifest.ID == gen.ID` — `StructuralResult{Passed: true}`.
   - `Sanity` with `DefaultSanityConfig()`: object ratio `42/40 = 1.05 >= 0.5` and chunk ratio `311/298 = 1.04 <= 3.0`, both pass; the parser error rate is always `0.0` (see Notes) — `StructuralResult{Passed: true}`.
   - `VersionCorrectness(ctx, embedder, vb, ns, gen, SampleChunks(chunks))`: `SampleChunks` sorts the 311 staged chunks by ID and takes the first 5. Each one's `Content` is embedded and sent as `QueryRequest{Vector: …, Text: content, TopK: 10, Filter: &backend.Filter{Generation: "gen_9f21ac"}}`. Every returned point has `Metadata.Version == "v1.9.1"` and `Metadata.Generation == "gen_9f21ac"` — `StructuralResult{Passed: true}`.

3. `report.Passed()` is `true`, so `gen.State = domain.GenReady`, persisted with an updated `UpdatedAt`. `Run` returns the updated `gen` and `report`.

4. The caller passes all three results into `promote.Promote(ctx, store, gen, "embedded", structural, sanity, versionCorrectness)` (`"embedded"` is the install's `vector.backend`, the active-generation key in every retrieval mode). Every result passed and `gen.State == domain.GenReady`, so it calls `store.PromoteGeneration(ctx, gen, "embedded")`.

5. `PromoteGeneration` (internal/control/bbolt/active_generations.go) runs one `bolt.Tx.Update`: it builds the key `go|github.com/spf13/cobra|v1.9.1|embedded`, finds nothing active for it (this is v1.9.1's first build), writes `gen_9f21ac` with `State: domain.GenActive`, and sets the pointer to `gen_9f21ac`. `gen_7a10bd` is untouched and stays active for `v1.8.0`. Had an earlier build of v1.9.1 been active, it would have been flipped to `SUPERSEDED` in the same transaction.

**Garbage-collecting an unreferenced version**

6. Some time later the last project on `v1.8.0` upgrades, and `retention.PlanGC` finds that version past its grace period with no remaining `project`/`latest`/`manual_pin` reference. It returns `retention.GCCandidate{Ecosystem: "go", Package: "github.com/spf13/cobra", Version: "v1.8.0", Reason: "grace_expired"}` — even though `gen_7a10bd` is still active, since being active no longer protects a version.

7. `gc.Run` calls `runOne`, which derives `id := JobID("gc", dep)` — `"job_" + hex(blake3("gc|go|github.com/spf13/cobra|v1.8.0")[:16])` — so a retried run resumes the same job. No job exists yet, so it creates one in `domain.JobPending`, flips it to `domain.JobRunning`, and persists it via `control.PutJob`.

8. `deleteCandidate` runs the fixed order: `control.ClearActiveGeneration(ctx, "go", "github.com/spf13/cobra", "v1.8.0", vb.Name())` removes the version's pointer (marking `gen_7a10bd` `SUPERSEDED`) so search stops resolving it; `vb.Delete` with `Filter{Ecosystem: "go", Dependency: "github.com/spf13/cobra", Version: "v1.8.0"}` removes the points from every index; `control.ListGenerationsByDependencyVersion` finds `gen_7a10bd`, and `data.DeleteGeneration` clears its Badger chunks and manifest; finally `control.DeleteGenerationRecord(ctx, "gen_7a10bd")` and `control.DeleteAllReferences(ctx, "go", "github.com/spf13/cobra", "v1.8.0")` remove the bbolt records.

9. On success, `runOne` sets `job.State = domain.JobSucceeded` and returns `Result{Candidate: candidate, Succeeded: true}`. Had a step failed, the job would be `domain.JobFailed` with `LastError` set and `Result.Error` carrying the message, without blocking the other candidates. The daemon then reclaims Badger's value-log space.

## Notes

- `validate.Structural` checks `manifest.ID == gen.ID` instead of the original sketch's nonexistent content-hash field, and "Badger's chunk count matches the manifest" instead of "every chunk's version metadata matches": per-chunk version metadata only exists on index points, which `VersionCorrectness` already checks, and a Badger-only check would false-positive on content reuse (internal/lifecycle/validate/validate.go).
- `Sanity`'s `MaxParserErrorRate` is unreachable in practice: `generation.Build` fails the whole generation on any single normalizer error, so a generation that reaches `VALIDATING` always has a 0.0 parse error rate. The threshold stays wired for config parity (internal/lifecycle/validate/sanity.go).
- `promote.Promote`'s `backendName` parameter wasn't in the original ticket's signature — it's needed because the active-generation pointer is backend-scoped, and `Promote` can't derive a backend name from a `domain.Generation` alone (internal/lifecycle/promote/promote.go).
- `gc.Run`, `RunOrphans` and `RunSupersededDuplicates` never return a non-nil error themselves — failures are per candidate, and every delete step is idempotent, so re-running resumes cleanly.
- The deletion order (index → Badger → bbolt) is load-bearing: bbolt's generation/reference records go last, so a crash mid-GC always leaves enough bbolt state to find what still needs cleanup.
- Clearing the active pointer first (ADR-012) means search never resolves a pointer to data that's mid-deletion, and no dangling pointer is left behind.
- `retention.PlanGC` discovers candidates via `ListAllReferences`, not a dedicated `dependency_versions` bucket — that fuller relational split (STORE-001) was never built (internal/retention/gc_planner.go).
