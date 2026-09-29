# POINT-002: Shared-content storage cost

**Epic:** Vector Point Identity
**Status:** done. Options 2 and version-exact refs shipped and measured; full-scale re-measurement complete (71.25% residual duplication, `Ref: "HEAD"` hypothesis refuted); migration path resolved with no new code (see Results). The residual duplication itself continues as [POINT-004](POINT-004-cross-project-generation-reuse.md), a distinct mechanism.

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
- **Measured live:** 13 dependencies (the 4 that had failed as `subdir not found`, 4 pseudo-versions, plain and monorepo ones): **13 of 13 synced, 0 failed, 0.7% duplicate points** (2,346 points, 2,329 distinct). **Version-exactness was then checked directly** on 10 of them: each dependency's stored commit equals an independently computed `git rev-parse` of its tag (hand-written expected tags, not the code under test), or the pseudo-version's commit — 10 of 10, including the `billing`/`workflows`/`resourcesettings` submodules, the `-deprecated` tag on `x/tools/cmd/cover`, and the `internal/ini` sub-path module. (The first attempt reported 3 failures; that was a flaw in the check, not the fix — see the next point.)
- **Side finding (reproduced through the real query service; latent, not fixed): a reused object carries the first creator's provenance.** Objects are stored once per distinct content (GEN-003), so a chunk whose text another dependency stored first (a shared LICENSE or README) resolves to that dependency's object, whose `SourceURI` and `Commit` name the *other* repository. `TestProvenanceOfAChunkSharedAcrossDependencies` (`internal/cli`) syncs two dependencies with an identical README and queries the second: **the search result is correct** (`Dependency`, `Version`, `Generation` come from the vector point, which is taken from the current generation), but **`GetProvenance` for that chunk names the first dependency's repo and commit**. Impact today: none visible — nothing in production calls `GetProvenance` (the MCP tools never do; only tests reference it). It would become a real defect the moment provenance is surfaced. The test skips, reporting the known issue, while the mismatch exists and becomes a real assertion once fixed. Fix options: (1) record per-generation provenance (`source_uri`, `commit`) on the chunk record, which is already per-generation, and read that first; (2) scope the content-hash reuse index per dependency, which fixes attribution at the cost of storing shared text once per dependency.
- **Still open:** (a) re-run the full-scale sync to see whether the 33.9% residual duplication disappears (hypothesis: it came from `HEAD`); (b) existing generations built at `HEAD` stay active until re-synced — no migration exists, a wipe is the only fix today (POINT-003 territory); (c) some dependencies that used to "succeed" at `HEAD` will now fail where the repo never tagged that version — correct, but the failure count will move; (d) options 3 to 5 above (shared rows, one sync per shared commit, smaller vectors) remain undecided and were not needed to fix the wrong-content bug.

**(a) resolved — see the "Full-scale re-measurement (2026-09)" section below.**

**(b) resolved, deliberately with no new code.** `domain.Generation` records no signal distinguishing a `HEAD`-resolved commit from a tag-resolved one — adding one would mean a schema change to detect a condition that, by definition, can only exist in a *pre-existing* production database from before this fix shipped, and no such database exists in any environment available to build or verify detection logic against (every live test this session has run started from a freshly wiped store). Building unverifiable migration-detection code would break this whole session's live-verification discipline for no real benefit. The actual migration path already exists and needs no new tooling: `ragctl sync` with no `--project` filter already re-syncs every registered project's every dependency; `--force` promotes past VAL-002's sanity ceiling if a stale generation's chunk counts differ sharply from the fresh one. An operator upgrading across this fix runs `ragctl sync --force` once, exactly as they would for any other correctness fix that changes what gets indexed. This is operational guidance, not a gap — recorded here so it's not silently forgotten as "still open."

**(c) not a task — an already-correctly-documented, accepted behavior change.** Nothing to build; the failure count moving is the intended outcome of refusing to guess, not a regression.

**(d) remains a genuine open decision, deliberately not picked unilaterally.** Options 3-5 (shared-row dedup, shared-commit sync, smaller vectors) trade real complexity (read-modify-write races, reference counting) against storage savings whose actual size depends on (a)'s answer — see the full-scale re-measurement below for whether there's still a real problem worth that complexity.

## Full-scale re-measurement (2026-09)

Re-ran the same real terraform corpus (11 Go modules, the same fixture as the original measurement) end to end with version-exact refs as the default (no code change needed — that's already how `goFallbackManifest` works since the earlier fix).

**(a) resolved, and the hypothesis is refuted — the residual duplication got worse under version-exact refs, not better.**

Measured directly against the real Qdrant collection after the full run completed: **2,943,286 total points, 846,099 distinct chunk IDs — 71.25% duplicate points**, up from 33.9% in the original `Ref: "HEAD"` measurement. Version-exact refs alone did not fix this; something else dominates at full 11-project scale.

Broke the duplicate points down by cause, using each chunk's owning `(dependency, version)` and `generation` payload fields:

| Cause | Extra points | % of total |
|---|---|---|
| Same `(dependency, version)` stored under **multiple different `generation` IDs** | 1,831,688 | **62.2%** |
| Different dependencies/versions whose content happens to be byte-identical (shared `LICENSE`/doc boilerplate across many real `cloud.google.com/go/*` submodules, etc.) | 265,499 | 9.0% |

The second bucket is not a bug — genuinely distinct packages legitimately sharing identical text. The first bucket is the real, still-open problem, and it is a *bigger* version of the original hypothesis's own shape (the same content re-synced repeatedly), not a new one. Concrete example: `cloud.google.com/go/appengine@v1.9.7` — one exact dependency@version — has **6,735 points spread across at least 5 different `generation` IDs**, confirmed via `points/count` and a payload sample. It should be one generation, reused by every one of the 11 terraform projects that references that exact version; instead each project's own sync appears to create its own independent generation for identical content.

**Revised hypothesis, not yet confirmed:** generation reuse/dedup happens (if at all) only *within* a single project's own sync, not *across* projects. With 11 projects in this corpus sharing many common dependencies, that gap compounds heavily at this scale — which is also consistent with why the earlier 13-dependency subset measurement (0.7% duplicates, single-project) didn't surface it. Filed as [POINT-004](POINT-004-cross-project-generation-reuse.md) to investigate the actual code path and design a fix; not folded into this ticket since it's a distinct mechanism from the `Ref: "HEAD"` bug this ticket was originally about.
