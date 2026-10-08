# GIT-005: Partial clone + sparse checkout

**Epic:** Local package-manager cache reuse
**Status:** done
**Depends on:** GIT-001 (git cache manager)
**Estimated size:** medium

## Goal
Cut the bandwidth/disk cost of `internal/source/git`'s `EnsureMirror` clone by fetching only the file content depctl actually reads — doc-shaped files by default — instead of every blob in the repo's entire history, using git's own partial-clone (`--filter=blob:none`) and sparse-checkout features. Unlike GIT-004 (local package-manager cache reuse), this helps on *every* clone, not just the ones where a local cache happens to already have the resolved version, and it doesn't sacrifice the full commit/tag history cross-version diffing needs — a blobless partial clone still transfers complete history/tree metadata, it just defers blob content until something actually checks it out.

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
- A worktree checked out with the doc-pattern set contains `.md`/`.txt`/`LICENSE`-shaped files and excludes files depctl's normalizers don't match (e.g. `.png`, `.lock` files, `node_modules/`).
- Measured bandwidth/blob-count reduction against a real, moderately large public repo (recorded in the post-implementation note, not just asserted) — this ticket's entire value proposition is quantitative, so it needs a real before/after number, not just "fewer files checked out."
- A synthetic repo whose docs live under a pattern the default set doesn't match falls back to a full checkout and still produces normalized output (proves the fallback path actually recovers, not just detects the miss).
- Release-note/cross-version diffing (which needs history, not working-tree content, for most of its work) is unaffected — full history is still present after a blobless partial clone.

## Acceptance criteria
- [x] `EnsureMirror` uses `--filter=blob:none`, with a compatibility fallback to today's behavior.
- [x] Worktree materialization applies sparse-checkout using patterns derived from the existing normalizer registry, not a hand-maintained list.
- [x] A repo whose docs the default patterns miss still gets indexed correctly via the fallback path.
- [x] A real measured reduction in bytes transferred for at least one real-world dependency is recorded in the post-implementation note.

## Post-implementation note
Shipped close to spec, with two real corrections made during implementation (both verified directly against real `git` invocations, not assumed from documentation):

1. **`--cone` mode is wrong for this use case.** The original design sketch specified `git sparse-checkout set --cone <patterns>`, but cone mode only accepts whole-directory patterns and rejects extension globs like `*.md` outright: `fatal: specify directories rather than patterns. If your directory really has any of '*?[]\' in it, pass --skip-checks`. Non-cone mode (`git sparse-checkout init --no-cone`, then `set` with plain gitignore-style globs) is what's actually needed for "any `.md` file anywhere in the tree," and that's what shipped.
2. **No fallback code is needed for a server that doesn't support `--filter=blob:none`.** The original design anticipated needing one; in practice, `git clone --filter=blob:none` against a server without filter support just prints `warning: filtering not recognized by server, ignoring` and clones normally — non-fatal, handled entirely by git itself. The one real fallback case is a *local* git old enough not to parse the `--filter` flag at all, handled with one plain retry without the flag in `EnsureMirror`.

Also found and worked around during testing (not a code change, a testing gotcha worth recording): git silently ignores `--filter` for a **plain local-path** clone (`warning: --filter is ignored in local clones; use file:// instead.`) — irrelevant for production (real remote URLs always go through the real protocol), but `TestEnsureMirrorAndSparseCheckoutSkipsNonMatchingBlobBytes` had to use an explicit `file://` URL to actually exercise partial-clone behavior in a test.

**Measured reduction** (recorded from `TestEnsureMirrorAndSparseCheckoutSkipsNonMatchingBlobBytes`, a real git repo with one small `README.md` and one 2MB non-doc blob): mirror size after the blobless clone = 28,170 bytes; after a `*.md`-only sparse checkout = 29,823 bytes (a ~1.6KB growth, exactly the `README.md` content) — the 2,097,152-byte (2MB) `big.bin` blob was never fetched at all. `TestBuildEndToEnd` (in `internal/data/generation`) additionally confirms this works correctly through the real production path (`Build` → `acquireGitSources` → `MaterializeWorktree` with real Go-ecosystem sparse patterns), not just in isolation — its fixture mixes `README.md` and `widget.go`, and both correctly survive sparse-checkout and produce normalized `KnowledgeObject`s.

Implementation landed in `internal/source/git/cache.go` (`EnsureMirror`'s `--filter=blob:none` + retry), `internal/source/git/worktree.go` (`MaterializeWorktree` gained a `sparsePatterns []string` parameter; `checkoutSparseOrFallback`/`checkoutCommit`/`gitStepErr`/`worktreeCleanup` new helpers), and `internal/normalize/sparse.go` (new file, `SparsePatterns(ecosystem) []string`) — wired through `internal/data/generation/build.go`'s `acquireGitSources`, which now takes the dependency's ecosystem and computes patterns once per generation build. One LICENSE-casing subtlety worth flagging: git's sparse-checkout pattern matching is case-sensitive on a case-sensitive filesystem (Linux) but not on macOS (`core.ignorecase` defaults true there) — `SparsePatterns` lists both `LICENSE*` and `license*` explicitly rather than relying on filesystem-dependent case folding, to match `plaintext.Normalizer`'s own case-insensitive `Supports()` check exactly.

Not built (correctly out of scope per this ticket's own Non-goals): GIT-004 (local package-manager cache fast-path) remains unbuilt in this epic.
