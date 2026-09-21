# POINT-002: Shared-content storage cost

**Epic:** Vector Point Identity
**Status:** options 2 and version-exact refs done and measured on a 13-dependency subset; full-scale re-measurement and a migration for old generations open (see Results)

## Problem
POINT-001 is correct but stores one row per (generation, chunk). On the 167-dependency terraform slice, **91% of rows are duplicates** (101,032 points, 9,668 distinct chunks): ~280 MB of extra vectors at 768 dims × 4 bytes = 3 KB each. The duplication is concentrated in monorepo submodules, e.g. ~100 `cloud.google.com/go/*` dependencies each storing ~600 identical repo-root points.

## Suspicion (confirmed)
Those submodule dependencies may be indexing the *repo root's* docs rather than their own folder's. The Go fallback manifest (`fallbackManifest`, internal/cli/sync.go) maps a module path to its git repo and uses `Ref: "HEAD"`; BOUND-001 skips nested modules, which leaves the root module's content for every submodule name. If confirmed, this is both wasted storage *and* wrong search results (and HEAD is not version-exact). Fixing it may remove most of the duplication and is a correctness fix in its own right.

## Options
1. **Accept the cost.** Simplest. Storage scales with generations × chunks.
2. **Scope submodule dependencies to their own subdirectory** (module path minus repo path = folder; sparse-checkout that folder; use the submodule's tag form `<dir>/vX.Y.Z`). Removes most duplicates *and* likely fixes wrong content. Needs the mapping to be right per ecosystem/repo layout.
3. **Shared rows with an owner list** (one point per unique text; payload lists owning generations). Smallest storage, but every write becomes read-modify-write (races under concurrent workers) and deletes need reference counting. High complexity.
4. **Sync a shared repo commit once and reference it** from every dependency that maps to it. Saves acquisition/parse work; does not by itself remove duplicate rows.
5. **Smaller vectors** (e.g. truncate to fewer dimensions if the embedding model supports it). Shrinks every row ~3x; quality trade-off must be measured. Independent of the others.

## Recommendation
Verify the suspicion, then do option 2. Consider option 5 separately once measured.

## Acceptance criteria
- [x] Confirm or refute: what content does `cloud.google.com/go/billing` actually index? (Confirmed: the repo root's.)
- [x] Re-measure duplicate ratio on the terraform fixture after the chosen change. (3% early, 33.9% at full scale; residual unexplained, see Results.)

## Results (option 2 built and measured)
- **Confirmed.** The suspicion was right: every `cloud.google.com/go/<x>` pointed at the repo root and indexed the root module's content (its nested modules skipped by BOUND-001). That was both the 91% duplication and the wrong docs.
- **Built:** `registry.Source.Subdir`, derived in `fallbackManifest` from the module path minus the repo's import root (a trailing `/vN` is dropped), scopes the sparse checkout (`normalize.ScopeToSubdir`) and the walk; a missing directory fails acquisition instead of indexing nothing. Tests: subdir derivation (vanity and GitHub paths, `/vN`), pattern scoping, and a real-git build proving a subdir source indexes only its own module's objects.
- **Measured live** (real terraform, N=5): early in the run duplicates were **3%** (30,399 points, 29,435 distinct); at the end of the full run **33.9%** (799,387 points, 528,665 distinct, 571 generations, 557 dependencies); Qdrant 3.0 GB. The rise from 3% to 34% is **not explained**. Hypothesis, unverified: fallback sources use `Ref: "HEAD"`, so every version of a dependency, and the same module reached through different terraform projects, indexes identical default-branch content. Confirming needs a count of distinct chunks per dependency name across generations.
- **Side effect worth knowing:** correct content is more content. The full store is larger than the old, wrong-content one (Qdrant 3.0 GB versus 1.3 GB at the point that run was killed), so the earlier "~30 MB if deduplicated" figure described the wrong data, not a target.
- **Version-exact refs (built after the full-scale run).** `fallbackManifest` now pins the version's own tag (`moduleTagTemplate`: `v${version}` at the repo root, `<subdir>/v${version}` for a submodule); `refCandidates` resolves a Go pseudo-version to its commit and strips `+incompatible`; a missing tag in a cached mirror triggers one `FetchTags` and retry (`FetchTags` had no caller before); a version with no tag fails to acquire with the ref it looked for, never falling back to a branch head. Tested against real git: each version indexes its own tag's content and never unreleased `main`, a pseudo-version resolves to its commit, a missing tag errors, and a version released after the mirror was cached is found via the refresh (that test fails with the refresh disabled).
- **Measured live:** 13 dependencies (the 4 that had failed as `subdir not found`, 4 pseudo-versions, plain and monorepo ones): **13 of 13 synced, 0 failed, 0.7% duplicate points** (2,346 points, 2,329 distinct). Version-exactness itself is covered by the unit tests above; it was not separately observed live.
- **Still open:** (a) re-run the full-scale sync to see whether the 33.9% residual duplication disappears (hypothesis: it came from `HEAD`); (b) existing generations built at `HEAD` stay active until re-synced — no migration exists, a wipe is the only fix today (POINT-003 territory); (c) some dependencies that used to "succeed" at `HEAD` will now fail where the repo never tagged that version — correct, but the failure count will move; (d) options 3 to 5 above (shared rows, one sync per shared commit, smaller vectors) remain undecided and were not needed to fix the wrong-content bug.
