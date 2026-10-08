# Feature: syncing a dependency version

This is depctl's core write path — turning "project X depends on package Y at version Z" into a validated, queryable set of indexed chunks. It's the one flow that touches every store (bbolt, Badger, and the search index — the vector store and/or the keyword index, depending on `retrieval.mode`) and almost every pipeline package. `docs/architecture.md`'s "`depctl plan` / `depctl sync` flow" and "`depctl gc` flow" sections cover the CLI-command boundary; this doc traces the same territory one layer deeper, across package boundaries, for a single `SYNC_VERSION` action end to end — including the failure/force paths that the per-command diagrams don't show.

Related package docs: [cli](../internal/cli.md), [planner](../internal/planner.md), [data-generation](../internal/data-generation.md), [source-git](../internal/source-git.md), [normalize](../internal/normalize.md), [data-fingerprint](../internal/data-fingerprint.md), [data-chunk](../internal/data-chunk.md), [embedding](../internal/embedding.md), [backend](../internal/backend.md), [lifecycle](../internal/lifecycle.md), [control](../internal/control.md), [data-badger](../internal/data-badger.md).

## Where the trigger comes from

`depctl sync` (`internal/cli/sync.go`), MCP's `sync_project` tool (enabled by default — see [query-serving](query-serving.md)), `search_dependency_docs`'s own JIT-sync-on-miss branch (WATCH-019, also in [query-serving](query-serving.md)), and `depctl watch` (`internal/cli/watch.go`, which re-resolves a project after one of its manifest files changes, then syncs just that project) all call the same exported `RunSync` (`internal/cli/sync.go`) — there is exactly one sync-execution code path in the binary. `RunSync` first calls `computePlans` (`internal/cli/plan.go`), which reads every registered project's `Resolution` and `VersionReference`s from bbolt and calls `planner.Plan` to diff them against the registry and any already-active generation. Only a `planner.ActionSyncVersion` action reaches the pipeline below; `ActionAddReference`/`ActionDropReference` are cheap bbolt/retention writes handled inline in `RunSync`'s loop (`internal/cli/sync.go`) and never touch generation/embedding/vector-backend code at all.

`RunSync` also takes an optional `*daemon.SyncPriority` (WATCH-020): a small FIFO a concurrent `BumpPriority` call (the daemon's `/v1/sync/priority` endpoint, reached by `search_dependency_docs` when a background sync for the project is already running) can push a dependency name onto. Before dispatching each action, `RunSync`'s loop drains it and calls `bumpActionToFront` (`internal/cli/sync.go`) to reorder that dependency's `SYNC_VERSION` action to the front of the remaining queue — the action still runs through the exact same pipeline below, just sooner. A `nil` `*SyncPriority` (the CLI's own `depctl sync` invocation) is a valid, always-empty no-op.

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
        Sync->>Sync: fallbackManifest(dep)\n(Go only: github.com/org/repo directly,\nelse resolveVanityImport go-import lookup)
        alt still no manifest
            Sync-->>Sync: error "no registry manifest for <dep>"
        end
    end

    Sync->>Gen: Create(ctx, dep)
    Gen->>Badger: PutManifest (empty skeleton, written first)
    Gen->>Bbolt: PutGeneration (PLANNED, gen_<ULID>)

    Sync->>Gen: Build(ctx, gen, manifest.Sources, gitCache)
    Gen->>Bbolt: setState ACQUIRING
    Gen->>Git: EnsureMirror (blobless: --filter=blob:none, GIT-005),\nResolveRef, MaterializeWorktree(sparsePatterns)\n(per type:git source, Ref templated with dep version)
    Git-->>Gen: worktree(s) (temp dirs;\nsparse checkout scoped to source's patterns,\nfalls back to a full checkout if sparse-checkout fails)
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
        opt embedder in use
            Rep->>Emb: Embed(texts)
        end
        Rep->>VB: Upsert(points)\nmetadata stamped from gen/sources, never the object
        Rep->>Bbolt: PutBackendReplica (progress after every batch)
    end

    Sync->>Val: Run(gen, manifest, replica, prior gen/manifest, embedder, vb)
    Val->>Val: Structural (counts self-consistent)
    Val->>Val: Sanity (vs. the most recently promoted generation\nof another version of this dependency; auto-pass if none)
    opt embedder in use
        Val->>Emb: Embed 5 sample chunks
    end
    Val->>VB: Query 5 samples (by vector or text) filtered to gen.ID
    Val->>Val: VersionCorrectness (every hit matches gen's version)
    Val->>Bbolt: setState VALIDATING -> READY or FAILED

    alt Sanity failed AND --force
        Sync->>Sync: override Sanity to Passed=true\n(Structural/VersionCorrectness never forceable)
    end
    alt any check still failing
        Sync-->>Sync: error "validation failed: [...]"
    else all pass
        Sync->>Prom: Promote(gen, backendName, results)
        Prom->>Bbolt: PromoteGeneration\n(one db.Update txn: supersede this exact version's\nprevious active gen, if any (a rebuild),\nactivate candidate, swap its active_generations pointer)
    end
```

### Which index a sync writes (`retrieval.mode`)

`VB` above is the CLI's `searchIndex` (`internal/cli/retrieval.go`): it presents every index the current mode writes as one `backend.VectorBackend`, so `Replicate`, validation and promotion don't know which indexes exist. `getPipeline()` picks the shape per run:

- **`keyword`**: a keyword-only pipeline with a nil embedder. Points carry `Text` (chunk content; API docs lead with their qualified symbol) and no vector, and the keyword index (`keyword.db`, plain-Go BM25) indexes them. Ollama is never contacted.
- **`auto`** (fresh installs): the keyword index plus, when Ollama and the vector store are both ready, the embedder and the vector store. If either isn't ready, the sync still succeeds keyword-only.
- **`vector`** (also what a config without the key means): embedder and vector store only; a sync fails if either isn't ready.

Before the action loop, `RunSync` backfills: in `auto`/`keyword` mode `backfillKeyword` adds keyword entries for versions built without them, and in `auto`/`vector` mode `backfillVectors` adds vectors to versions first built keyword-only, once embeddings are available (both via `generation.AddToIndex`). Both are best-effort; a failure is printed and retried on the next sync.

## State across the three stores, at each stage

| Stage | bbolt (`control/bbolt`) | Badger (`data/badger`) | Vector backend |
|---|---|---|---|
| `Create` | `Generation{PLANNED}` written | empty `Manifest` skeleton written | untouched |
| `Build` (ACQUIRING→NORMALIZING→INDEXING) | `Generation.State` advances | `KnowledgeObject`s, `Chunk`s, content-hash index, final `Manifest` counts | untouched |
| `Replicate` | `BackendReplica` progress per batch | chunks read back (to get text for embedding and keyword indexing) | points upserted, namespace ensured |
| `validate.Run` | `Generation.State` → `VALIDATING`/`READY`/`FAILED` | manifest read for count comparisons | queried (not written) to prove points are real and correctly tagged |
| `promote.Promote` | one transaction: this version's previous active generation (if any) superseded, candidate activated, its `active_generations` pointer swapped. Other versions of the dependency stay active (ADR-012) | untouched | untouched |

## Failure and skip paths

- **`--dry-run`**: `RunSync` is never called at all — `cli.runSync` asks the daemon for its plan, prints it and returns (`internal/cli/sync.go`). Nothing in this doc's pipeline executes.
- **`--offline`**: each `SYNC_VERSION` action is reported `SKIP` and counted, without ever calling `getPipeline()` — no embedder or search index is constructed and vector backfill is skipped, so a fully offline `sync` run makes zero network calls even indirectly (keyword backfill still runs; it's local) (`internal/cli/sync.go`).
- **Lazy pipeline construction**: `getPipeline()` (`internal/cli/sync.go`) builds the embedder and search index exactly once, on the first action that needs it, and reuses them for the rest of the run (the git cache is passed in by the caller) — a plan with zero `SYNC_VERSION` actions and nothing to backfill never probes the embedder's `Dimensions()`, which is itself a live call.
- **Any `Build`/`Replicate` stage failure**: the generation is marked `FAILED` with the error persisted (wrapped in `ErrAcquisition`/`ErrNormalization`/`ErrReplication`); `syncVersion` returns the error, `RunSync` reports `FAIL` for that action and continues to the next one — one dependency's failure never aborts the whole `sync` run.
- **`--force`**: overrides only a `Sanity` (VAL-002) failure — a plausible-but-flagged count change. `Structural` and `VersionCorrectness` failures always block promotion; they indicate a broken replica, not something a human judgment call should override.
- **No registry manifest**: `fallbackManifest` (`internal/cli/sync.go`) derives a single `git` source for a Go module, tagged `Authority: 0` (still `TrustRepository`, since the content itself isn't less authoritative — only its ranking) — either directly, when the module path is already shaped like `github.com/org/repo`, or via `resolveVanityImport`'s `go-import` meta-tag lookup (GIT-008) for anything else. Anything else with no manifest match, or a non-Go ecosystem, fails immediately with "no registry manifest for `<dep>`", never reaching `generation.Create`.
- **Sparse checkout falls back to a full checkout**: when a `git` source specifies `sparsePatterns` (GIT-005) and `git sparse-checkout` itself fails against the mirror, `MaterializeWorktree` (`internal/source/git/worktree.go`) retries with a plain, unfiltered checkout rather than failing the whole `Build` step — a slower worktree, not a broken sync.
