# GIT-005: Partial clone + sparse checkout

**Epic:** Local package-manager cache reuse
**Status:** backlog
**Depends on:** GIT-001 (git cache manager)
**Estimated size:** medium

## Goal
Cut the bandwidth/disk cost of `internal/source/git`'s `EnsureMirror` clone by fetching only the file content ragctl actually reads — doc-shaped files by default — instead of every blob in the repo's entire history, using git's own partial-clone (`--filter=blob:none`) and sparse-checkout features. Unlike GIT-004 (local package-manager cache reuse), this helps on *every* clone, not just the ones where a local cache happens to already have the resolved version, and it doesn't sacrifice the full commit/tag history cross-version diffing needs — a blobless partial clone still transfers complete history/tree metadata, it just defers blob content until something actually checks it out.

## Non-goals
- No change to what gets *indexed* — this only changes what gets *fetched* to feed the same normalizers that already exist (`internal/normalize/*`). Indexing scope is explicitly out of scope for this whole conversation's changes.
- No per-project whitelist as the primary mechanism — see Design below for why the normalizer registry itself is a better default than a user-maintained list.
- No change to `internal/source/git`'s public API shape beyond `EnsureMirror`/`worktree.go`'s internals — callers (`generation/build.go`) shouldn't need to know sparse-checkout is happening underneath.

## Simplicity constraints
- One new sparse-checkout pattern set, derived mechanically from the normalizer registry's already-existing `Supports()` matchers (`.md`/`.mdx`, `.txt`/`.rst`, `LICENSE*`, known changelog filenames) plus `**/*.go` for Go projects (needed for the godoc extraction pass) — not a new normalizer-registry abstraction, just a function that asks the existing registry what it wants.
- Fallback, not a hard requirement: if sparse-checkout ever produces an empty or clearly-wrong tree for a repo (nonstandard doc layout, e.g. `Documentation/` with unfamiliar extensions), fall back to a full checkout for that repo rather than silently indexing nothing. Detect via a cheap "did normalizeSources find literally zero files" check already implicit in the existing pipeline.

## Design
- `Cache.EnsureMirror` (`internal/source/git/cache.go:87`) adds `--filter=blob:none` to the `git clone --mirror` args. This is a pure win regardless of sparse-checkout — even a full-tree normalization pass only needs the blobs for the specific commit being checked out, not the whole history's worth.
- `worktree.go`'s `git worktree add --detach` step gains a preceding `git sparse-checkout set --cone <patterns>` (run once per worktree, since sparse-checkout state lives in the worktree's own `.git/info/sparse-checkout`, not the shared bare mirror).
- Patterns come from a new `internal/normalize.SparsePatterns(ecosystem) []string` (or similar) that asks the registry which filename/extension patterns any registered normalizer would match, plus ecosystem-specific extras (`**/*.go` for godoc extraction on Go projects) — computed once, not maintained by hand per project.
- A future project-level override (an explicit list of extra paths to always include, e.g. a repo that keeps its real docs in `Documentation/*.adoc`) is a natural follow-up once this lands, but isn't required for the first version — see Non-goals.

## Inputs / Outputs
- Input: a repository URL and a specific commit/version being materialized for normalization (unchanged from today).
- Output: the same worktree directory `normalizeSources` already reads from, containing fewer files (only what the sparse patterns matched) but is otherwise identical in effect.

## Failure behavior
- Sparse-checkout produces zero matched files (a repo with all documentation under paths/extensions the pattern set doesn't cover): fall back to a full (non-sparse) checkout of the same worktree and log that the fallback happened, so this is discoverable rather than a silent "no knowledge for this dependency" outcome.
- `--filter=blob:none` unsupported by the local git version or the remote host: fall back to today's unfiltered `--mirror` clone — this is a network/tooling compatibility fallback, not a per-repo content decision.

## Tests
- A worktree checked out with the doc-pattern set contains `.md`/`.txt`/`LICENSE`-shaped files and excludes files ragctl's normalizers don't match (e.g. `.png`, `.lock` files, `node_modules/`).
- Measured bandwidth/blob-count reduction against a real, moderately large public repo (recorded in the post-implementation note, not just asserted) — this ticket's entire value proposition is quantitative, so it needs a real before/after number, not just "fewer files checked out."
- A synthetic repo whose docs live under a pattern the default set doesn't match falls back to a full checkout and still produces normalized output (proves the fallback path actually recovers, not just detects the miss).
- Release-note/cross-version diffing (which needs history, not working-tree content, for most of its work) is unaffected — full history is still present after a blobless partial clone.

## Acceptance criteria
- [ ] `EnsureMirror` uses `--filter=blob:none`, with a compatibility fallback to today's behavior.
- [ ] Worktree materialization applies sparse-checkout using patterns derived from the existing normalizer registry, not a hand-maintained list.
- [ ] A repo whose docs the default patterns miss still gets indexed correctly via the fallback path.
- [ ] A real measured reduction in bytes transferred for at least one real-world dependency is recorded in the post-implementation note.
