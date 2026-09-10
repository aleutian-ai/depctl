# GIT-001: Git cache manager

**Epic:** Git Acquisition
**Status:** done
**Depends on:** REG-003
**Estimated size:** medium

## Goal
Maintain a local bare-mirror Git cache per repository, fetching tags/refs on demand and resolving a version string to a commit, using the system `git` binary.

## Non-goals
- Does not implement the Git wire protocol — always shells out to `git`.
- Does not materialize working trees (GIT-002).

## Simplicity constraints
- One bare mirror per repository URL, shared across all dependency versions of that package — do not clone per-version.
- No custom Git object parsing; every operation is a `git` subcommand via `internal/executil` (RES-002).

## Design
- Package: `internal/source/git`
- Cache path: `~/.local/share/ragctl/git/<host>/<org>/<repo>.git` (mirrors design spec example, e.g. `github.com/grpc/grpc-go.git`).
- Core operations:
  ```go
  type Cache struct { root string }
  func (c *Cache) EnsureMirror(ctx context.Context, url string) (repoPath string, err error)
  func (c *Cache) FetchTags(ctx context.Context, repoPath string) error
  func (c *Cache) ResolveRef(ctx context.Context, repoPath, ref string) (commit string, err error)
  ```
- `EnsureMirror`: if the target path doesn't exist, `git clone --mirror <url> <path>`; if it exists, no-op (fetch is separate/explicit via `FetchTags`).
- `FetchTags`: `git fetch --tags` inside the bare repo.
- `ResolveRef`: `git rev-parse <ref>^{commit}` to resolve a tag/ref (e.g. `v1.75.1` from the manifest's `ref: "v${version}"` template) to a commit SHA.
- Ref templating (`${version}` substitution) happens at the call site using REG-003's `Source.Ref`, not inside the cache manager.

## Inputs / Outputs
- Input: repository URL (from a manifest `Source`), a version string to resolve.
- Output: local bare-mirror path; resolved commit SHA.

## Failure behavior
- Network failure on clone/fetch: typed transient error, retryable by the job system (later milestone) — this ticket just needs to return an error that's identifiable as network-related (e.g. wraps `executil` exit code/stderr).
- Unresolvable ref (tag doesn't exist): typed permanent error — do not retry blindly.

## Tests
- Initial fetch against a small local bare test repo fixture (created via `git init --bare` + a couple of commits/tags in test setup, no real network needed).
- Incremental fetch: second `FetchTags` call succeeds and picks up new tags added to the fixture "remote".
- Offline read after fetch: once tags are fetched, `ResolveRef` works without further network access (verified by pointing to a local fixture path, no real offline simulation needed).

## Acceptance criteria
- [x] Initial fetch creates a bare mirror at the documented path convention.
- [x] Incremental fetch updates tags without re-cloning.
- [x] `ResolveRef` works purely from local cache after a fetch (no repeated network calls).

## Post-implementation fix (epic 14 adversarial review)
`EnsureMirror` had no locking around its check-then-clone sequence. Two concurrent first-time `EnsureMirror` calls for the same repository URL (a real possibility once a planner drives multiple dependency builds concurrently, epic 15) could both pass the `os.Stat` miss and both run `git clone --mirror` into the same target directory at once — and the mirror is a resource shared across every generation of that dependency, not scoped to one build, so a corrupted mirror from a race would silently break every future build of that dependency, not just the racing calls. Fixed with a per-repo-path `sync.Mutex` (`Cache.lockMirror`), held for the whole check-then-clone sequence. Regression test: `TestConcurrentEnsureMirrorOfSameRepoDoesNotCorruptMirror` runs 8 concurrent `EnsureMirror` calls against one fixture repo and confirms every one succeeds with an identical, usable mirror (`ResolveRef` against it afterward, not just presence).
