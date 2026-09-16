# internal/source/git

`internal/source/git` is ragctl's local Git acquisition layer: it maintains one shared bare mirror per repository URL, resolves refs to commits, materializes disposable worktrees at a given commit for normalization to read, and computes file-level deltas between two commits. Every operation shells out to the system `git` binary via `internal/executil` — no Git wire-protocol or object parsing is implemented directly. It sits between dependency resolution (which decides *which* version to fetch) and `internal/normalize` (which reads files out of a materialized worktree), and is driven by `internal/data/generation.Build`'s `ACQUIRING` phase.

## Key types and functions

- `Cache` — manages a local bare-mirror cache of Git repositories, one mirror per repo URL shared across every dependency version of that package. `internal/source/git/cache.go`
- `NewCache(dir string) *Cache` — returns a `Cache` rooted at `dir` (e.g. `<data-dir>/git`). `internal/source/git/cache.go`
- `(*Cache) EnsureMirror(ctx, rawURL) (string, error)` — clones `rawURL` as a bare mirror under the cache root if one doesn't already exist; existing mirrors are left untouched. The clone is blobless (`git clone --mirror --filter=blob:none`, GIT-005) — file/tree metadata for every ref is fetched, but blob content is fetched lazily, on demand, only for what a later checkout actually touches. Holds a per-repo-path lock across its whole check-then-clone sequence to prevent concurrent double-clones. `internal/source/git/cache.go`
- `(*Cache) FetchTags(ctx, repoPath) error` — fetches new tags/refs into an existing bare mirror. `internal/source/git/cache.go`
- `(*Cache) ResolveRef(ctx, repoPath, ref) (string, error)` — resolves a tag/branch/commit-ish to a commit SHA purely from local cache (`git rev-parse <ref>^{commit}`), no network access. `internal/source/git/cache.go`
- `(*Cache) MaterializeWorktree(ctx, repoPath, commit, sparsePatterns []string) (string, func() error, error)` — checks out `commit` into a fresh temp dir via `git worktree add --detach`; returns a cleanup func callers must defer. Cleanup always runs on `context.Background()`, not the caller's context, so cancellation can't leave a dangling worktree. When `sparsePatterns` is non-empty (GIT-005), only matching files are fetched/checked out (non-cone `git sparse-checkout`, paired with `EnsureMirror`'s blobless clone so only the matched blobs are ever pulled) — falling back to a full checkout transparently if the patterns match nothing in that commit's tree. `internal/source/git/worktree.go`
- `(*Cache) Delta(ctx, repoPath, oldCommit, newCommit) ([]FileDelta, error)` — computes the file-level diff between two commits (`git diff --name-status`) as an optimization hint for normalization, not the authoritative change signal (that's content hashing). Empty `oldCommit` diffs against Git's empty-tree SHA, so a first sync reports every file as added. `internal/source/git/delta.go`
- `FileDelta` / `FileStatus` — one file's change between two commits (`Path`, `OldPath` set only for renames, `Status` ∈ `added`/`modified`/`deleted`/`renamed`). `internal/source/git/delta.go`,`internal/source/git/delta.go`
- `CacheError{Op, Kind, Cause}` — typed error wrapping every operation failure with a retryability classification; unwraps to `Cause`. `internal/source/git/errors.go`
- `ErrorKind` (`ErrKindTransient` / `ErrKindPermanent`) — clone/fetch failures are transient (network, retryable); `ResolveRef`/`MaterializeWorktree`/`Delta` failures are permanent (bad ref/commit, retrying won't help). `internal/source/git/errors.go`

## Dataflow

```mermaid
sequenceDiagram
    participant Gen as generation.Build (ACQUIRING)
    participant Cache as git.Cache
    participant FS as local cache dir / temp dirs
    participant Norm as internal/normalize

    Gen->>Cache: EnsureMirror(ctx, repoURL)
    Cache->>Cache: lockMirror(repoPath)
    alt mirror already exists
        Cache-->>Gen: repoPath
    else first acquisition
            Cache->>FS: git clone --mirror --filter=blob:none repoURL repoPath
        Cache-->>Gen: repoPath
    end

    Gen->>Cache: FetchTags(ctx, repoPath)
    Cache->>FS: git fetch --tags

    Gen->>Cache: ResolveRef(ctx, repoPath, ref)
    Cache->>FS: git rev-parse ref^{commit}
    Cache-->>Gen: commit SHA

    Gen->>Cache: MaterializeWorktree(ctx, repoPath, commit, sparsePatterns)
    Cache->>FS: git worktree add --detach [--no-checkout] tmpDir commit
    opt sparsePatterns non-empty (GIT-005)
        Cache->>FS: git sparse-checkout init --no-cone; set <patterns>; checkout
        Note over Cache,FS: empty result -> sparse-checkout disable,\nfull checkout instead
    end
    Cache-->>Gen: worktreeDir, cleanup()

    Gen->>Norm: walk worktreeDir, normalize files
    Note over Gen,Norm: NORMALIZING phase consumes worktree files

    opt incremental sync (future)
        Gen->>Cache: Delta(ctx, repoPath, oldCommit, newCommit)
        Cache->>FS: git diff --name-status
        Cache-->>Gen: []FileDelta
    end

    Gen->>Cache: cleanup() (deferred)
    Cache->>FS: git worktree remove --force tmpDir
```

## Walkthrough

Scenario: `generation.Build`'s `ACQUIRING` phase needs the grpc-go repository at tag `v1.68.0` — continuing from registry.md's example, where the manifest's git source resolved to URL `https://github.com/grpc/grpc-go` and ref `v1.68.0`. `Cache` is rooted at `<data-dir>/git`, e.g. `/Users/jin/.local/share/ragctl/git`.

1. **`EnsureMirror` computes the mirror path.** `EnsureMirror(ctx, "https://github.com/grpc/grpc-go")` first calls `mirrorPath(c.root, rawURL)`, which delegates to `splitGitURL` — since the URL has a real scheme and host, that branch returns `host = "github.com"`, `path = "/grpc/grpc-go"` directly from `url.Parse` (no SCP-like or local-path handling needed here) (/Users/jin/GolandProjects/ragctl/internal/source/git/cache.go). `mirrorPath` trims the `.git` suffix (none present), splits into segments `["grpc", "grpc-go"]`, appends `.git` to the last segment, and joins: `repoPath = "/Users/jin/.local/share/ragctl/git/github.com/grpc/grpc-go.git"` (/Users/jin/GolandProjects/ragctl/internal/source/git/cache.go).

2. **Per-repo locking, then clone.** `defer c.lockMirror(repoPath)()` takes a `sync.Mutex` keyed on that exact `repoPath` string (/Users/jin/GolandProjects/ragctl/internal/source/git/cache.go, 76) — so a second, concurrent `EnsureMirror` call for the same grpc-go repo (e.g. triggered by two dependency versions of it in different projects being built at once) blocks here rather than racing a second `git clone --mirror` into the same directory. `os.Stat(repoPath)` finds nothing on a first acquisition, so `os.MkdirAll` creates the parent dirs and `executil.Run` shells out to:

   ```
   git clone --mirror --filter=blob:none https://github.com/grpc/grpc-go /Users/jin/.local/share/ragctl/git/github.com/grpc/grpc-go.git
   ```

   with a 10-minute timeout (`defaultCloneTimeout`) (/Users/jin/GolandProjects/ragctl/internal/source/git/cache.go). On success, `repoPath` is returned; a non-zero exit code (e.g. network failure, repo renamed) removes the partial clone and returns a `*CacheError{Op: "EnsureMirror", Kind: ErrKindTransient, ...}` — transient because retrying a clone/fetch is expected to be worth it, unlike a bad ref.

3. **FetchTags, then ResolveRef.** `FetchTags(ctx, repoPath)` runs `git fetch --tags` in that mirror directory to pick up any tags pushed since the mirror was created (/Users/jin/GolandProjects/ragctl/internal/source/git/cache.go). Then `ResolveRef(ctx, repoPath, "v1.68.0")` runs `git rev-parse v1.68.0^{commit}` purely against the local mirror — no network — and returns the trimmed stdout, e.g. `"7f6a3c1e2b8d4f0a9c5e6b7d8f9a0b1c2d3e4f5a"` (a realistic 40-char SHA) (/Users/jin/GolandProjects/ragctl/internal/source/git/cache.go). A ref that doesn't exist produces `ErrKindPermanent` — retrying won't fix a nonexistent tag.

4. **MaterializeWorktree.** With that commit SHA, `MaterializeWorktree(ctx, repoPath, "7f6a3c1e...")` first creates a fresh temp directory via `os.MkdirTemp("", "ragctl-worktree-*")`, e.g. `/tmp/ragctl-worktree-482913567`, then runs:

   ```
   git worktree add --detach /tmp/ragctl-worktree-482913567 7f6a3c1e2b8d4f0a9c5e6b7d8f9a0b1c2d3e4f5a
   ```

   in `repoPath` (/Users/jin/GolandProjects/ragctl/internal/source/git/worktree.go). On success it returns `(worktreeDir, cleanup, nil)`, where `cleanup` is a closure the caller must defer. The grpc-go source tree — `README.md`, `stats/`, `interop/`, etc. — now sits at `/tmp/ragctl-worktree-482913567` exactly as it existed at that commit.

5. **Normalization reads the worktree.** `generation.normalizeSources` walks `/tmp/ragctl-worktree-482913567` with `filepath.WalkDir`, building a `domain.SourceSnapshot` per file (e.g. `LocalPath: "/tmp/ragctl-worktree-482913567/README.md"`, `LogicalPath: "README.md"`, `Commit: "7f6a3c1e..."`) and handing each to `internal/normalize` (see normalize.md's walkthrough, which picks up exactly this `README.md`).

6. **Cleanup.** When the caller's deferred `cleanup()` runs, it executes `git worktree remove --force /tmp/ragctl-worktree-482913567` in `repoPath`, always on `context.Background()` so a cancelled build context can't abandon the worktree on disk. If that fails (e.g. the directory was already deleted externally), it falls back to `os.RemoveAll` plus `git worktree prune`, and only then surfaces an error — cleanup failure is reported, not panicked on (/Users/jin/GolandProjects/ragctl/internal/source/git/worktree.go).

Note that `Delta` (file-level diffs between two commits, e.g. for a hypothetical incremental resync from an older grpc-go commit to `7f6a3c1e...`) has no caller in this path today — as the Notes below say, it's an optimization hint with no current wiring into `generation.Build`.

## Notes

- Mirror layout is `<root>/<host>/<org>/<repo>.git` (`mirrorPath`, `internal/source/git/cache.go`); `splitGitURL` also accepts SCP-like syntax (`git@host:org/repo.git`) and plain local filesystem paths (rooted under a fixed `"local"` host), which is how the package's own tests and `hack/fetch-corpus` drive it without a real remote.
- `EnsureMirror`'s per-repo-path mutex (`lockMirror`, `internal/source/git/cache.go`) was added during an epic-14 adversarial review after it found two concurrent first-time acquisitions of the same repo could both run `git clone --mirror` into the same directory, corrupting a mirror shared by every future generation of that dependency — not just the racing calls. There is no cross-process lock, only in-process (`sync.Mutex` in the `Cache` struct), and no cache eviction — mirrors are never pruned.
- `MaterializeWorktree`'s cleanup deliberately runs on `context.Background()` rather than the caller's context — reviewed and confirmed correct by design (not a leak), since a cancelled caller must still be able to remove its own worktree. A failed `git worktree remove` falls back to `rm -rf` + `git worktree prune` and only surfaces an error to the caller rather than panicking.
- `Delta` is described in its own doc comment as "an optimization hint for normalization — not the authoritative change signal, which remains content hashing" — `internal/data/generation`'s dedup logic keys on `chunk.ContentHash`, not on `Delta` output. As of this writing `Delta` has no in-repo caller in `internal/data/generation.Build`; the `ACQUIRING` phase only calls `EnsureMirror`/`FetchTags`/`ResolveRef`/`MaterializeWorktree` (see `docs/architecture.md`, "internal/data/generation" section).
- Wired into `ragctl sync`/`ragctl watch`/MCP's `sync_project` and `search_dependency_docs`'s JIT-sync branch, all via `internal/data/generation.Build`'s `ACQUIRING` phase (see [sync](../features/sync.md)) — this package is no longer exercised only by `hack/fetch-corpus`.
- `sparsePatterns` (GIT-005) come from `normalize.SparsePatterns(ecosystem)` — the same doc-shaped file patterns the normalizers already read — not a per-manifest field; `generation.Build`'s `acquireGitSources` derives them once per ecosystem and passes them to every git source's `MaterializeWorktree` call.
