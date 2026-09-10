# internal/lifecycle

`internal/lifecycle` has no top-level `.go` files — it is purely a grouping directory for the three packages that move a generation through its post-build states: `validate` (deterministic pre-promotion checks), `promote` (atomic candidate → ACTIVE transition), and `gc` (executing `internal/retention`'s garbage-collection plan). Together they are the "is this generation good, and can it replace what's currently serving retrieval" and "is this old generation safe to delete" halves of the generation lifecycle, sitting after `internal/data/generation.Build`/`Replicate` and before/around `internal/query`.

## Key types and functions

**internal/lifecycle/validate**
- `StructuralResult` — shared pass/fail-with-reasons shape every check returns (internal/lifecycle/validate/validate.go).
- `Structural(ctx, gen, manifest, replica, badgerStore)` — VAL-001: non-zero counts, backend point count == manifest chunk count, manifest ID == generation ID, Badger's actual staged chunk count == manifest's claim (internal/lifecycle/validate/validate.go).
- `SanityConfig`, `DefaultSanityConfig()` — three configurable guardrail thresholds: `MinObjectCountRatio` (0.5), `MaxChunkCountRatio` (3.0), `MaxParserErrorRate` (0.05) (internal/lifecycle/validate/sanity.go).
- `Sanity(ctx, candidate, prior, candidateManifest, priorManifest, cfg)` — VAL-002: compares candidate's object/chunk counts against the dependency's previously active generation; auto-passes if `prior` is nil (first-ever generation) (internal/lifecycle/validate/sanity.go).
- `SampleChunks(chunks)` — deterministically picks up to 5 chunks, sorted by ID, for the version-correctness smoke test (internal/lifecycle/validate/version_correctness.go).
- `VersionCorrectness(ctx, embedder, vb, ns, gen, sampleChunks)` — VAL-003: re-embeds sampled chunks' own text, queries the vector backend filtered to `gen.ID`, asserts every returned point's version/generation metadata matches `gen`'s — proves the replica isn't cross-contaminated and isn't missing its own content (internal/lifecycle/validate/version_correctness.go).
- `Report` — bundles all three results; `Passed()` and `Failures()` (internal/lifecycle/validate/run.go).
- `Run(ctx, gen, manifest, replica, prior, priorManifest, cfg, embedder, vb, ns, store, badgerStore)` — orchestrator: flips `gen.State` to `VALIDATING`, runs all three checks, then to `READY` or `FAILED` (persisting `Report.Failures()` joined into `gen.Error` on failure) (internal/lifecycle/validate/run.go).

**internal/lifecycle/promote**
- `ErrValidationFailed`, `ErrNotReady` — sentinel errors (internal/lifecycle/promote/promote.go).
- `Promote(ctx, store, candidate, backendName, results...)` — VAL-004: checks every supplied `StructuralResult.Passed`, checks `candidate.State == READY`, then calls `bbolt.Store.PromoteGeneration` in one write transaction — no network/remote calls, since acquisition/embedding/replication already happened upstream (internal/lifecycle/promote/promote.go).

**internal/lifecycle/gc**
- `ControlStore`, `DataStore` — narrow consumer-side interfaces over `*bbolt.Store`/`*badger.Store` (internal/lifecycle/gc/gc.go).
- `JobID(jobType, dep)` — deterministic `job_<blake3 hex>` ID from job type + dependency version, so re-running GC against the same candidate resumes the same job (internal/lifecycle/gc/gc.go).
- `Result` — per-candidate outcome: `Candidate`, `Succeeded`, `Error` (internal/lifecycle/gc/gc.go).
- `Run(ctx, control, data, vb, ns, plan)` — RET-004: for each `retention.GCCandidate` in `plan`, claims/resumes a job and deletes in fixed order (internal/lifecycle/gc/gc.go).
- `runOne`/`deleteCandidate` — per-candidate job-state machine (`PENDING`→`RUNNING`→`SUCCEEDED`/`FAILED`) and the three-step delete: vector replica (filtered by ecosystem/dependency/version) → Badger generation data (per generation ID) → bbolt generation records + reference metadata (internal/lifecycle/gc/gc.go).

## Dataflow

```mermaid
flowchart TD
    Build["generation.Build / Replicate"] -->|gen, manifest, replica| Run["validate.Run"]
    Run --> Structural["Structural check\n(counts, backend/manifest/Badger agreement)"]
    Run --> Sanity["Sanity check\n(vs prior active generation)"]
    Run --> VC["VersionCorrectness check\n(re-embed sample, query vb, assert metadata)"]
    Structural --> Report["validate.Report"]
    Sanity --> Report
    VC --> Report
    Report -->|Passed/Failures| StoreState["bbolt: gen.State = READY or FAILED"]
    Report -->|StructuralResult...| Promote["promote.Promote"]
    Promote -->|candidate.State == READY?| PromoteGeneration["bbolt.Store.PromoteGeneration\n(candidate -> ACTIVE, supersede prior)"]

    RetentionPlan["retention.PlanGC\n(references + grace expiry + active-gen check)"] -->|[]GCCandidate| GCRun["gc.Run"]
    GCRun --> Job["claim/resume job (bbolt)"]
    Job --> DelVec["1. vb.Delete (vector backend)"]
    DelVec --> DelBadger["2. data.DeleteGeneration (Badger)"]
    DelBadger --> DelBbolt["3. control.DeleteGenerationRecord +\nDeleteAllReferences (bbolt)"]
    DelBbolt --> JobDone["job.State = SUCCEEDED"]
```

`validate.Run` is called after `generation.Build`/`Replicate` produce a candidate generation, manifest, and `domain.BackendReplica`; its three checks feed a `Report` that both persists `gen.State` and is handed to `promote.Promote` as `validate.StructuralResult`s. `promote.Promote` is the sole gate into `ACTIVE` state — it does no I/O beyond one bbolt write. Separately, `retention.PlanGC` (in `internal/retention`, not `internal/lifecycle`) reads bbolt reference/grace-period state and active-generation pointers to compute `[]retention.GCCandidate`; `gc.Run` consumes that plan and performs the actual three-store deletion, entirely independent of the validate/promote path — GC only ever targets *superseded, no-longer-referenced* versions, never a candidate mid-validation.

## Walkthrough

**Validating and promoting `gen_9f21ac` for `github.com/spf13/cobra@v1.9.1`**

1. `generation.Build`/`Replicate` have already produced a candidate `domain.Generation{ID: "gen_9f21ac", State: domain.GenBuilt, Dependency: {Dependency: {Ecosystem: "go", Name: "github.com/spf13/cobra"}, Version: "v1.9.1"}}`, a `generation.Manifest{ID: "gen_9f21ac", Sources: [...1 source...], ObjectCount: 42, ChunkCount: 311}`, and a `domain.BackendReplica{PointCount: 311}`. The dependency's previously active generation is `gen_7a10bd` (`ObjectCount: 40, ChunkCount: 298`).

2. `validate.Run` (internal/lifecycle/validate/run.go) flips `gen.State = domain.GenValidating` and persists it via `store.PutGeneration` (internal/lifecycle/validate/run.go), then runs the three checks in sequence:
   - `Structural(ctx, gen, manifest, replica, badgerStore)` (internal/lifecycle/validate/validate.go): 311 == 311 == 311 across manifest, replica, and Badger's own `ListGenerationChunks` count, and `manifest.ID == gen.ID` ("gen_9f21ac" == "gen_9f21ac") — all pass, so `StructuralResult{Passed: true}` (internal/lifecycle/validate/validate.go).
   - `Sanity(ctx, candidate, prior, candidateManifest, priorManifest, cfg)` (internal/lifecycle/validate/sanity.go), with `cfg = DefaultSanityConfig()` (`MinObjectCountRatio: 0.5, MaxChunkCountRatio: 3.0`): object ratio `42/40 = 1.05 >= 0.5` and chunk ratio `311/298 = 1.04 <= 3.0`, both pass; `parserErrorRate` is the hardcoded `0.0` (internal/lifecycle/validate/sanity.go), also under the 0.05 ceiling — `StructuralResult{Passed: true}`.
   - `VersionCorrectness(ctx, embedder, vb, ns, gen, SampleChunks(chunks))` (internal/lifecycle/validate/version_correctness.go): `SampleChunks` sorts the 311 staged chunks by ID and takes the first 5 (internal/lifecycle/validate/version_correctness.go). Each of those 5 chunks' `Content` is embedded, then queried against `vb` with `Filter: &backend.Filter{Generation: "gen_9f21ac"}`, `TopK: 10`. Every returned point's `Metadata.Version == "v1.9.1"` and `Metadata.Generation == "gen_9f21ac"` — `StructuralResult{Passed: true}`.

3. `report := Report{Structural: structural, Sanity: sanity, VersionCorrectness: versionCorrectness}` (internal/lifecycle/validate/run.go). `report.Passed()` is `true`, so `gen.State = domain.GenReady` (internal/lifecycle/validate/run.go), persisted with an updated `UpdatedAt` (internal/lifecycle/validate/run.go). `Run` returns the updated `gen` and `report`.

4. The caller passes all three `StructuralResult`s into `promote.Promote(ctx, store, gen, "qdrant-prod", structural, sanity, versionCorrectness)` (internal/lifecycle/promote/promote.go). Every result's `Passed` is `true` and `gen.State == domain.GenReady`, so both guard checks (internal/lifecycle/promote/promote.go) pass through to `store.PromoteGeneration(ctx, gen, "qdrant-prod")` (internal/lifecycle/promote/promote.go).

5. `PromoteGeneration` (internal/control/bbolt/active_generations.go) runs one `bolt.Tx.Update`: it builds the pointer key `activeGenerationKey("go", "github.com/spf13/cobra", "qdrant-prod")` → `"go|github.com/spf13/cobra|qdrant-prod"` (internal/control/bbolt/active_generations.go), looks it up in `active_generations`, finds it currently points at `gen_7a10bd`, loads that record and flips it to `domain.GenSuperseded` (internal/control/bbolt/active_generations.go), then writes `gen_9f21ac` into the `generations` bucket with `State: domain.GenActive` (internal/control/bbolt/active_generations.go), and finally repoints `active_generations["go|github.com/spf13/cobra|qdrant-prod"] = "gen_9f21ac"` (internal/control/bbolt/active_generations.go). All three writes commit atomically or not at all.

**Garbage-collecting a superseded candidate**

6. Some time later `retention.PlanGC` decides `gen_7a10bd`'s dependency version (`go`, `github.com/spf13/cobra`, `v1.8.0` — an older version than the one just promoted, now past its grace period with zero references) is a `retention.GCCandidate{Ecosystem: "go", Package: "github.com/spf13/cobra", Version: "v1.8.0"}`, and hands `[]retention.GCCandidate{candidate}` to `gc.Run`.

7. `gc.Run` (internal/lifecycle/gc/gc.go) calls `runOne` for the candidate, which derives `id := JobID("gc", dep)` — a deterministic `"job_" + hex(blake3("gc|go|github.com/spf13/cobra|v1.8.0")[:16])` (internal/lifecycle/gc/gc.go) — so a retried GC run resumes the same job instead of double-deleting. Since no job exists yet, it creates one in state `domain.JobPending`, immediately flips it to `domain.JobRunning`, and persists it via `control.PutJob` (internal/lifecycle/gc/gc.go).

8. `deleteCandidate` (internal/lifecycle/gc/gc.go) runs the fixed three-step order: `vb.Delete` with `Filter: &backend.Filter{Ecosystem: "go", Dependency: "github.com/spf13/cobra", Version: "v1.8.0"}` removes the vector points; `control.ListGenerationsByDependencyVersion` finds the one generation ID this version ever produced (`gen_7a10bd`) and `data.DeleteGeneration(ctx, "gen_7a10bd")` clears its Badger chunk/object data; then `control.DeleteGenerationRecord(ctx, "gen_7a10bd")` and `control.DeleteAllReferences(ctx, "go", "github.com/spf13/cobra", "v1.8.0")` remove the bbolt generation record and every reference row for that version (internal/lifecycle/gc/gc.go).

9. On success, `runOne` sets `job.State = domain.JobSucceeded` and persists it, returning `Result{Candidate: candidate, Succeeded: true}` (internal/lifecycle/gc/gc.go). Had step 8 failed partway — say `vb.Delete` succeeded but `DeleteAllReferences` errored — `job.State` would be set to `domain.JobFailed` with `LastError` populated, and `Result.Error` would carry the message, without blocking any other candidate in the plan (internal/lifecycle/gc/gc.go).

## Notes

- `validate.Structural` deliberately diverges from its original ticket sketch: it checks `manifest.ID == gen.ID` instead of a nonexistent content-hash field, and substitutes "Badger's actual chunk count matches the manifest's claim" for "every chunk's version metadata matches," because per-chunk version metadata only exists once chunks are replicated into a vector backend — that's exactly what `VersionCorrectness` already checks, and a Badger-only substitute would false-positive on legitimate GEN-003 content reuse (internal/lifecycle/validate/validate.go).
- `Sanity`'s `MaxParserErrorRate` is currently unreachable in practice: `generation.Build` fails the whole generation on any single normalizer error rather than tolerating a per-file failure rate, so a generation that reaches `VALIDATING` always has a 0.0 parse error rate. The check and threshold are still wired up for config parity if `Build` ever starts tolerating partial failures (internal/lifecycle/validate/sanity.go, 62-65).
- `promote.Promote`'s `backendName` parameter isn't in the original ticket's sketched signature — it was added because the active-generation pointer is inherently backend-scoped (mirrors VEC-003), and `Promote` can't derive a backend name from a `domain.Generation` alone (internal/lifecycle/promote/promote.go).
- `gc.Run` never returns a non-nil error itself — failures are captured per-candidate in `Result.Error`, and one candidate's deletion failure does not block the rest of the plan (internal/lifecycle/gc/gc.go). Every delete step is idempotent, so re-running `Run` after a partial failure resumes cleanly.
- `gc.deleteCandidate`'s three-step order (vector → Badger → bbolt) is fixed and load-bearing: bbolt's generation/reference records are the last things deleted, so a crash mid-GC always leaves enough bbolt state to identify what still needs cleanup on retry.
- `retention.PlanGC` discovers its candidate set via `ListAllReferences`, not a dedicated `dependency_versions` bucket — that fuller relational split (STORE-001) was never built; every version a project has resolved to already has a reference row once `planner.Plan`/`sync` records it (internal/retention/gc_planner.go).
