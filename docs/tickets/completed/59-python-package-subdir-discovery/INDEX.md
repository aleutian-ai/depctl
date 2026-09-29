# Epic: Python package subdirectory discovery

**Found live:** a fast, targeted structured-extraction check (docs/tickets/completed/57-js-python-structured-api-normalizers's own post-implementation validation, this session) against real, mem0-confirmed PyPI dependencies. `openai==2.30.0` and `requests==2.32.3` correctly produced `structured=true` `symbol_doc` objects (NORM-009 working as designed). `pydantic==2.12.5` — one of the most widely used PyPI packages, heavily docstring-documented — produced **zero** structured objects.

## Root cause, confirmed by reading the code (not yet reproduced against pydantic's real repo directly)

`internal/normalize/pydoc/entrypoint.go`'s `entryModuleFile(dir)` looks for `<dir>/__init__.py` directly at the acquired worktree's root, then falls back to a single/first top-level `.py` file — never recursing into a subdirectory. This already matches REG-013's own disclosed Non-goal ("No `Subdir` support — unlike npm, there's no equivalent registry-provided monorepo-directory field to key off of; a Python package published from a monorepo subdirectory isn't detectable from the PyPI API alone").

The real-world consequence this session's live check surfaces: this isn't only a *monorepo* problem. It also breaks the extremely common, non-monorepo Python layout where the actual package lives in a same-named subdirectory of the repo root (e.g. `pydantic/pydantic/__init__.py`, not `pydantic/__init__.py` at the repo root, alongside `pyproject.toml`/`README.md`/`tests/` etc. at that root) — a layout convention, not a monorepo. `entryModuleFile` finds nothing at the root and produces zero structured objects, silently (matching the designed "nothing found, no error" behavior — correct behavior given the gap, but the gap itself affects a large, ordinary share of real PyPI packages, not just a monorepo edge case).

## Why this is a distinct, real epic and not a two-line fix

Unlike Node's `discoverNodeSubdir` (epic 56/REG-012), which searches the tree for the `package.json` that declares the exact dependency name via a structural `git.Cache.ListFiles`/`ReadFile` scan, Python has no per-package manifest file inside the module directory itself to search for by name — `pyproject.toml`/`setup.py` (if present at all) sit at the repo root already, and a subpackage directory's own `__init__.py` carries no declared "package name" field to match against the way `package.json`'s `"name"` does. A structurally equivalent discovery mechanism needs a different signal — most plausibly: the dependency's own PyPI-declared name, normalized (hyphens/underscores, case), matched against top-level directory names in the repo tree (`pydantic` → look for a `pydantic/` directory containing `__init__.py`), falling back to the repo root when no such directory exists (today's only behavior, kept as the fallback, not replaced).

## Tickets
- [SUBDIR-001](SUBDIR-001-python-package-subdir-discovery.md) — **done.** Discovery itself (following REG-012/`discoverNodeSubdir`'s structural-search precedent, adapted for Python's name-normalization-based matching signal) was straightforward and worked on the first pass. Live-verifying against real `pydantic` past the fixture tests found a second, more serious bug the fixture tests couldn't see: scoping *every* sparse pattern (including README/LICENSE) to the discovered subdirectory broke acquisition outright for packages, like real `pydantic`, whose real docs live at the true repo root, not duplicated inside the source subdirectory — see the ticket's own two-pass post-implementation note.

## Non-goals
- Not a general monorepo `Subdir` mechanism spanning arbitrary layouts — scoped to the specific, common "package lives in a same-named (normalized) subdirectory of the repo root" shape, matching what real PyPI packages actually do.
- No change to REG-013's acquisition-time git-tag resolution — this is purely about *where in the tree* to look once a commit is already resolved, the same boundary epic 57 itself drew for Node.

## Evidence
- Live check that found this epic (this session): `pydantic==2.12.5` synced successfully (content acquired, README/CHANGELOG-derived objects present) but `structured=false` — confirmed via `hack/verify-sync`.
- `openai==2.30.0`, `requests==2.32.3` (same check, same run): both `structured=true`, confirming NORM-009 itself works correctly when the entry module is actually reachable at the repo root.
- **Final result, after SUBDIR-001 shipped**, re-verified live against real `pydantic==2.12.5` again: sync succeeds, real content restored (95 objects, 1,162 chunks — matching the original pre-regression baseline exactly). `structured=false` **still holds**, but for a different, already-understood reason: real `pydantic`'s `__init__.py` uses `__getattr__`-based lazy re-exports (a deliberate pydantic v2 pattern), which no static AST tool can resolve — a genuine, separate NORM-009 limitation (its own ticket already discloses "a dynamic export recorded as a limitation not fabricated"), not something this epic ever aimed to fix. SUBDIR-001's real, achieved goal was "find the right subdirectory and never silently lose content doing it" — confirmed.

## Status
**Epic can move to `completed/`** — SUBDIR-001 is done, live-verified twice (once finding the second bug, once confirming the full fix), and the epic's own scope (subdirectory discovery, not dynamic-export resolution) is fully delivered.
