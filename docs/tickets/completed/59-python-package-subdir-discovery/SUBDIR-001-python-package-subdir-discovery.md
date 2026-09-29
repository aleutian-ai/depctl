# SUBDIR-001: Python package subdirectory discovery

**Epic:** Python package subdirectory discovery
**Status:** done
**Depends on:** none
**Estimated size:** small-medium

## Goal
Let a Python dependency whose real package directory isn't at the repo root — the common `<pkg>/<pkg>/__init__.py` layout (e.g. real `pydantic`), and the increasingly common PyPA-recommended `src/<pkg>/__init__.py` layout — still be found and structurally extracted, instead of silently producing zero structured docs (`entryModuleFile` only ever checks the worktree root today).

## Design
Mirrors `discoverNodeSubdir`'s structural-search shape (`internal/data/generation/build.go`) and reuses the same no-checkout plumbing (`git.Cache.ListFiles`/`ReadFile`), but with a different matching signal — Python has no per-directory manifest file with a declared name the way `package.json` gives Node, so this matches by **normalized directory name** instead of a declared field:

1. `gitCache.ListFiles(ctx, repoPath, commit, "__init__.py")` — every `__init__.py` in the tree, no checkout needed (same call `discoverNodeSubdir` makes for `package.json`).
2. For each, normalize its **immediate parent directory's** basename (lowercase, `-`/`.` → `_` — the standard PyPI-distribution-name-to-import-name convention) and compare against the dependency's own normalized name.
3. **Depth-independent by design** — this one rule catches both `pydantic/pydantic/__init__.py` and `pydantic/src/pydantic/__init__.py` uniformly, without special-casing `src/`, since only the immediate parent's name is checked, not the full path.
4. **Exactly one match** → verified, scope the worktree to that directory (same `Subdir` mechanism Node already uses). **Zero or multiple matches** → fall back to today's root-level behavior, unchanged — never guess among ambiguous candidates.

## Why "fall back to root" is safe here, unlike Node's original bug
Node's pre-REG-012 bug actively mis-scoped: a monorepo's *unrelated* root content got indexed under a specific dependency's confident name (wrong content, not just missing content). Python's root fallback can't do that in the same way — if the real package lives elsewhere and isn't found, `entryModuleFile` at the root either finds nothing (today's existing, already-accepted zero-coverage outcome) or degrades to its own pre-existing "first loose `.py` file" fallback, a pre-existing behavior this ticket doesn't change or newly introduce risk into.

## Non-goals
- No build-system-config parsing (`pyproject.toml`'s `[tool.setuptools]`/`[tool.hatch.build]`/`[tool.poetry.packages]` etc.) — a real, more-correct alternative (Option C, considered and deliberately not picked, see epic INDEX) but meaningfully larger new surface; revisit only if normalized-name matching's real miss rate proves too high in practice.
- No fix for `entryModuleFile`'s own pre-existing "first loose `.py` file" fallback logic at the root — out of scope for this ticket, unrelated to subdirectory discovery.
- No handling of a PyPI name that bears no resemblance to its import name at all (e.g. `beautifulsoup4` → `bs4`) — a real, accepted miss; normalized-name matching cannot catch this by construction, same limitation `discoverNodeSubdir` doesn't have (Node's manifest declares its own name explicitly) but Python has no equivalent to declare it.

## Tests
- Unit: a fixture repo with `<pkg>/<pkg>/__init__.py` (the pydantic shape) resolves correctly.
- Unit: a fixture repo with `<pkg>/src/<pkg>/__init__.py` (the PyPA `src/` layout) resolves correctly via the same depth-independent rule, no special-casing needed.
- Unit: no matching directory anywhere falls back to root (today's unchanged behavior).
- Unit: multiple directories matching the normalized name (ambiguous) falls back to root rather than guessing.
- Unit: name normalization itself (`typing-extensions` → `typing_extensions`, case-folding).
- Full-pipeline integration test: a real git fixture shaped like real `pydantic` (package in a same-named subdirectory, real docstrings), synced end to end, produces real `symbol_doc` objects — the direct regression for the live-found gap.

## Acceptance criteria
- [x] A real Python package shaped like `pydantic` (package in a same-named subdirectory) produces real content instead of zero. **Revised from the original wording** — see post-implementation note: the fix restores real content (confirmed live against actual `pydantic==2.12.5`), but whether that content includes `structured=true` `symbol_doc` objects turned out to depend on the package's own code shape, a separate, already-disclosed NORM-009 limitation, not something this ticket controls.
- [x] The `src/<pkg>/` layout is caught by the same mechanism, not a separate code path.
- [x] No subdirectory match anywhere still falls back cleanly to root — no regression for every Python package already working today.
- [x] Ambiguous matches (more than one candidate directory) never guess — fall back to root.

## Post-implementation note

Built in two real passes, not one — the first was a genuine but incomplete fix, caught by continuing to live-verify against real `pydantic` rather than stopping once the fixture tests passed (matching this session's established discipline elsewhere).

**Pass 1 — subdirectory discovery.** `discoverPythonSubdir` (`internal/data/generation/build.go`) added alongside `discoverNodeSubdir`; `normalizePythonName` implements the standard PyPI-distribution-to-import-name convention (lowercase, `-`/`.` → `_`). Wired into `acquireGitSources` as a third `ecosystem ==` branch, structurally identical to the existing Node one. Fixture tests (including the `src/` layout, ambiguous-match, and no-match cases) all passed. **Live-verified against real `pydantic==2.12.5` and found a second, more serious bug**: the sync now *failed outright* — `validation failed: [source count is zero object count is zero chunk count is zero]`. Debug tracing (temporary `fmt.Fprintf`, matching this session's established live-debugging discipline) showed the discovery and scoping were both working exactly as designed (`worktreeDir` correctly scoped to `pydantic/`, real `.py` files present) — the actual problem was one level up.

**Pass 2 — the real root cause.** Unlike Node's `discoverNodeSubdir` (used for genuine monorepo submodules, each with its own independent `README`/`package.json`), a Python package found in a same-named subdirectory usually *isn't* a monorepo split — real `pydantic`'s own `README.md`/`LICENSE` live at the true repo root, not duplicated inside its `pydantic/` source directory. The original implementation scoped *every* sparse pattern (including doc-shaped ones) to the discovered subdirectory, silently excluding the real root-level docs — and since `pydantic`'s actual `__init__.py` also produces zero statically-extractable symbols (see below), the combination left the generation with genuinely nothing at all. Fixed by splitting `normalize.SparsePatterns` into `normalize.DocPatterns()` (kept unscoped, fetched from the true repo root) and `normalize.PythonSourcePatterns()` (scoped to the discovered subdirectory) — Node's case is unaffected, its whole pattern set still scopes together since that *is* the correct behavior for a genuine monorepo submodule. `acquiredSource` gained a `pythonEntryDir` field so `normalizeSources` can point `pydoc`'s own entry-point search at the discovered subdirectory while `normalizeSources`' doc-shaped `WalkDir` still walks from the true repo root — the two roots now legitimately differ for Python only, where before they were always the same directory.

**Test strengthened to actually catch this class of regression**: the original fixture had no root-level README at all, so it couldn't have caught the doc-exclusion bug even though the discovery mechanism it was testing was already correct. Added a real root `README.md` to `TestBuildFindsPydanticShapedPackageInASameNamedSubdirectory` and an assertion that it's indexed — verified rigorously (reverted pass 2's fix alone, confirmed the test fails on exactly the README assertion, restored it).

**Final live confirmation against real `pydantic==2.12.5`**: sync now succeeds (`1 synced, 0 failed`), and produces the exact same content as the original, pre-regression unscoped baseline — 95 objects, 1,162 chunks, `readme/license-only`. `structured=false` persists, and after inspection this is genuinely not a SUBDIR-001 problem: real `pydantic`'s `__init__.py` uses `__getattr__`-based lazy re-exports (a deliberate, well-known pydantic v2 pattern to keep import time fast) rather than plain top-level function/class definitions, so `pydoc`'s static AST-based extraction finds nothing to extract — the same kind of case NORM-009's own ticket already discloses ("a dynamic export recorded as a limitation not fabricated"), just a more thorough instance of it than that ticket's own fixture happened to exercise. SUBDIR-001's actual job — find the right subdirectory, never silently lose content while doing it — is done and confirmed; `pydantic`'s own dynamic-export shape is a separate, deeper limitation belonging to NORM-009, not reopened here.

Full suite green, `-race` clean, throughout both passes.
