# Feature: syncing a dependency version

This is ragctl's core write path — turning "project X depends on package Y at version Z" into a validated, queryable set of vector-embedded chunks. It's the one flow that touches every store (bbolt, Badger, the vector backend) and almost every pipeline package. `docs/architecture.md`'s "`ragctl plan` / `ragctl sync` flow" and "`ragctl gc` flow" sections cover the CLI-command boundary; this doc traces the same territory one layer deeper, across package boundaries, for a single `SYNC_VERSION` action end to end — including the failure/force paths that the per-command diagrams don't show.

Related package docs: [cli](../internal/cli.md), [planner](../internal/planner.md), [data-generation](../internal/data-generation.md), [source-git](../internal/source-git.md), [normalize](../internal/normalize.md), [data-fingerprint](../internal/data-fingerprint.md), [data-chunk](../internal/data-chunk.md), [embedding](../internal/embedding.md), [backend](../internal/backend.md), [lifecycle](../internal/lifecycle.md), [control](../internal/control.md), [data-badger](../internal/data-badger.md).

## Where the trigger comes from

`ragctl sync` (`internal/cli/sync.go`) and MCP's disabled-by-default `sync_project` tool both call the same exported `RunSync` (`internal/cli/sync.go`) — there is exactly one sync-execution code path in the binary. `RunSync` first calls `computePlans` (`internal/cli/plan.go`), which reads every registered project's `Resolution` and `VersionReference`s from bbolt and calls `planner.Plan` to diff them against the registry and any already-active generation. Only a `planner.ActionSyncVersion` action reaches the pipeline below; `ActionAddReference`/`ActionDropReference` are cheap bbolt/retention writes handled inline in `RunSync`'s loop (`internal/cli/sync.go`) and never touch generation/embedding/vector-backend code at all.

## The pipeline for one `SYNC_VERSION` action

```mermaid
sequenceDiagram
    participant Sync as cli.syncVersion
    participant Reg as internal/registry.Registry
    participant Gen as data/generation.Create/Build
    participant Git as source/git.Cache
    participant Norm as internal/normalize
    participant FP as data/fingerprint
    participant Chunk as data/chunk
    participant Bbolt as control/bbolt.Store
    participant Badger as data/badger.Store
    participant Rep as data/generation.Replicate
    participant Emb as embedding.Embedder
    participant VB as backend.VectorBackend
    participant Val as lifecycle/validate.Run
    participant Prom as lifecycle/promote.Promote

    Sync->>Reg: Match(ecosystem, package)
    alt no manifest match
        Sync->>Sync: fallbackManifest(dep)\n(Go github.com/org/repo paths only)
        alt still no manifest
            Sync-->>Sync: error "no registry manifest for <dep>"
        end
    end

    Sync->>Gen: Create(ctx, dep)
    Gen->>Badger: PutManifest (empty skeleton, written first)
    Gen->>Bbolt: PutGeneration (PLANNED, gen_<ULID>)

    Sync->>Gen: Build(ctx, gen, manifest.Sources, gitCache)
    Gen->>Bbolt: setState ACQUIRING
    Gen->>Git: EnsureMirror, ResolveRef, MaterializeWorktree\n(per type:git source, Ref templated with dep version)
    Git-->>Gen: worktree(s) (temp dirs)
    Gen->>Bbolt: setState NORMALIZING
    Gen->>Norm: walk worktrees, Registry.Select / godoc.New per Go dir
    Norm-->>Gen: []domain.KnowledgeObject
    Gen->>Bbolt: setState INDEXING
    Gen->>FP: Fingerprint + ObjectID per object
    Gen->>Badger: GetContentHashIndex (GEN-003 dedup check)
    alt content hash already known
        Gen->>Gen: reuse existing object ID\n(ObjectsReused++)
    else new content
        Gen->>Badger: PutKnowledgeObject, PutContentHashIndex\n(ObjectsCreated++)
    end
    Gen->>Chunk: Registry dispatch (markdown/symbol chunker)
    Gen->>Badger: PutChunk per chunk, PutManifest (final counts)
    Note over Gen: Build returns; gen left in INDEXING state

    Sync->>Rep: Replicate(ctx, gen, manifest.Sources, embedder, vb, ns)
    Rep->>VB: EnsureNamespace
    Rep->>Badger: ListGenerationChunks
    loop batches of 64
        Rep->>Badger: GetKnowledgeObject (parent, cached per call)
        Rep->>Emb: Embed(texts)
        Rep->>VB: Upsert(points)\nmetadata stamped from gen/sources, never the object
        Rep->>Bbolt: PutBackendReplica (progress after every batch)
    end

    Sync->>Val: Run(gen, manifest, replica, prior gen/manifest, embedder, vb)
    Val->>Val: Structural (counts self-consistent)
    Val->>Val: Sanity (vs. prior active generation; auto-pass if none)
    Val->>Emb: Embed 5 sample chunks
    Val->>VB: Query filtered to gen.ID
    Val->>Val: VersionCorrectness (every hit matches gen's version)
    Val->>Bbolt: setState VALIDATING -> READY or FAILED

    alt Sanity failed AND --force
        Sync->>Sync: override Sanity to Passed=true\n(Structural/VersionCorrectness never forceable)
    end
    alt any check still failing
        Sync-->>Sync: error "validation failed: [...]"
    else all pass
        Sync->>Prom: Promote(gen, backendName, results)
        Prom->>Bbolt: PromoteGeneration\n(one db.Update txn: supersede old active gen,\nactivate candidate, swap active_generations pointer)
    end
```

## State across the three stores, at each stage

| Stage | bbolt (`control/bbolt`) | Badger (`data/badger`) | Vector backend |
|---|---|---|---|
| `Create` | `Generation{PLANNED}` written | empty `Manifest` skeleton written | untouched |
| `Build` (ACQUIRING→NORMALIZING→INDEXING) | `Generation.State` advances | `KnowledgeObject`s, `Chunk`s, content-hash index, final `Manifest` counts | untouched |
| `Replicate` | `BackendReplica` progress per batch | chunks read back (to get text for embedding) | points upserted, namespace ensured |
| `validate.Run` | `Generation.State` → `VALIDATING`/`READY`/`FAILED` | manifest read for count comparisons | queried (not written) to prove points are real and correctly tagged |
| `promote.Promote` | one transaction: old active generation superseded, candidate activated, `active_generations` pointer swapped | untouched | untouched |

## Failure and skip paths

- **`--dry-run`**: `RunSync` is never called at all — `cli.runSync` prints `computePlans`' output and returns (`internal/cli/sync.go`). Nothing in this doc's pipeline executes.
- **`--offline`**: each `SYNC_VERSION` action is reported `SKIP` and counted, without ever calling `getPipeline()` — no embedder/vector-backend/git-cache is constructed, so a fully offline `sync` run makes zero network calls even indirectly (`internal/cli/sync.go`).
- **Lazy pipeline construction**: `getPipeline()` (`internal/cli/sync.go`) builds the embedder/vector-backend/git-cache exactly once, on the first action that needs it, and reuses it for the rest of the run — a plan with zero `SYNC_VERSION` actions never probes the embedder's `Dimensions()`, which is itself a live call.
- **Any `Build`/`Replicate` stage failure**: the generation is marked `FAILED` with the error persisted (wrapped in `ErrAcquisition`/`ErrNormalization`/`ErrReplication`); `syncVersion` returns the error, `RunSync` reports `FAIL` for that action and continues to the next one — one dependency's failure never aborts the whole `sync` run.
- **`--force`**: overrides only a `Sanity` (VAL-002) failure — a plausible-but-flagged count change. `Structural` and `VersionCorrectness` failures always block promotion; they indicate a broken replica, not something a human judgment call should override.
- **No registry manifest**: `fallbackManifest` (`internal/cli/sync.go`) derives a single `git` source for a Go module shaped like `github.com/org/repo`, tagged `Authority: 0` (still `TrustRepository`, since the content itself isn't less authoritative — only its ranking). Anything else with no manifest match fails immediately with "no registry manifest for `<dep>`", never reaching `generation.Create`.
