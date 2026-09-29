# NORM-009: Python static signature/docstring normalizer

**Epic:** JS/TS and Python structured API normalizers
**Status:** done
**Depends on:** none for implementation (this normalizer can be built and correctness-tested entirely against local fixtures). Its own *live, uncurated-PyPI* acceptance test depends on REG-013 (`docs/tickets/completed/56-npm-pypi-fallback-manifest`) — without automatic PyPI acquisition, there's no real, uncurated package to point it at. These are separate claims: don't read "no dependency" as "REG-013 doesn't matter here."
**Estimated size:** medium

## Post-implementation note

Shipped `internal/normalize/pydoc`, exactly as designed — the one ticket in this epic where the recommended tooling (Python's stdlib `ast` module, no third-party package) turned out to be *more* capable than the hand-rolled scanner NORM-008 needed, not less: `ast.unparse()` (Python 3.9+) renders a function/class header directly from the parsed AST — defaults, annotations, `*args`/`**kwargs`, positional/keyword-only markers all correct for free, no regex signature reconstruction needed at all. `ast.get_docstring()` likewise handles docstring extraction/dedenting natively. The result is a smaller, more robust script than `extract.js`, with no live-found parser bugs during development (unlike NORM-008's module-doc/symbol-doc misattribution bug, caught and fixed before shipping).

All four of the tightened spec's requirements delivered as designed: (1) `__all__` that isn't a statically-literal list is recorded as `exports_dynamic: true` on the `package_doc` object rather than silently dropped or fabricated; (2) single-hop relative-import re-exports (`from .module import Name`) are resolved against the sibling file, verified live against a real package (`requests`: `put`/`patch` resolved from `api.py`, `TooManyRedirects`/`ReadTimeout` from `exceptions.py`); (3) a `.pyi` stub's signature wins over the `.py` source's when both exist, but the `.py` docstring is kept when the stub's own is empty; (4) zero execution of the target package under any circumstance — verified live by never `pip install`-ing `requests` at all, purely static parsing of its cloned source.

Golden fixture (`internal/normalize/pydoc/normalize_test.go`) and full-pipeline integration test (`internal/data/generation/build_test.go`'s `TestBuildExtractsStructuredPythonDocsThroughTheFullPipeline`) both pass: correct signatures/docstrings, `_`-prefixed private symbols excluded, re-export resolved, dynamic-`__all__` correctly flagged rather than guessed. No sibling-package-leak fixture (unlike NORM-008's) — not applicable yet: REG-013's own Non-goals state Python has no `Subdir`/monorepo-directory support at all, so there's no dynamic scoping step that could leak a sibling's content the way `discoverNodeSubdir`'s absence could for Node.

**Live consequence, found this session (2026-09), not at ship time:** a fast targeted check against real mem0-confirmed PyPI dependencies (`docs/tickets/completed/56-npm-pypi-fallback-manifest/INDEX.md`'s own Results section) found `pydantic==2.12.5` — one of the most widely used PyPI packages — produces zero structured objects, while `openai==2.30.0`/`requests==2.32.3` from the same check both worked correctly. Root cause: `entryModuleFile` (`internal/normalize/pydoc/entrypoint.go`) only ever looks for `__init__.py` at the acquired worktree's own root; pydantic's real `__init__.py` sits in a `pydantic/` subdirectory of its repo, a common real layout, not a monorepo edge case. Tracked as its own epic rather than folded into this ticket: `docs/tickets/completed/59-python-package-subdir-discovery` — Node's `discoverNodeSubdir` structural-search precedent doesn't directly transfer (Python has no per-directory manifest file with a declared name to match against), so it needs its own design.

Live-verified against a real, uncurated PyPI package (`requests` 2.32.3, via REG-013): 65 structured objects extracted, including correctly-resolved re-exports and class methods.

## Goal
Extract real, per-symbol API documentation (exported functions/classes/methods, with signatures and docstrings) from a Python dependency's own worktree — the Python equivalent of `godoc` (NORM-004) and NORM-008's JS/TS counterpart — **without ever importing or executing the target package's code.**

## Design

### Why static-only is a hard requirement, not a preference
Sphinx/autodoc and stdlib `pydoc` both work by *importing* the target module and introspecting the live object — which means running arbitrary third-party code, including its transitive dependencies' code, as part of a sync. That's unacceptable for a dependency ragctl doesn't control the provenance of (a private, unfamiliar, or simply not-yet-installed-in-this-environment package) — the same principle REG-013 already applied to acquisition (no `pip install`, only static registry/git lookups). **griffe** (the `mkdocstrings` ecosystem's extractor) already solves this the right way: pure `ast`-module static analysis, never imports the target.

### Tooling decision (explicit, not defaulted into)
Two real options, both requiring a `python3` subprocess (Python's own `ast` module has no Go equivalent, and reimplementing full Python grammar in Go is out of proportion to this ticket):
- **Shell out to `griffe` itself** (`pip install griffe`, or vendor it) via its CLI (`griffe dump`) or a tiny wrapper script calling its Python API, emitting JSON. Reuses a real, maintained, already-correct implementation rather than reinventing it.
- **A small hand-written script using only Python's stdlib `ast` module** — no `griffe` dependency, mirroring `godoc`'s own "stdlib only, no external static-analysis dependency" precedent, at the cost of reimplementing a narrower slice of what griffe already does well (docstring style parsing, inherited-member handling).

**Recommendation for v1: the stdlib-`ast` path**, matching `godoc`'s own stated principle exactly (NORM-004: "stdlib only, no external static-analysis dependency") — the one case in this epic where the pure-Go precedent has a direct, equally-static Python-stdlib equivalent, so there's no real reason to accept a third-party Python package as a runtime dependency on top of the `python3` subprocess requirement this ticket already accepts. Detect `python3` availability at sync time; its absence is a clean per-dependency skip, not a sync failure — same graceful-degradation shape as NORM-008's `node` check.

### Extraction shape
- One object per module (its own module-level docstring, if any) — the `package_doc` equivalent.
- One object per exported top-level function/class/method, each carrying its rendered signature (parameters, defaults, type annotations exactly as written — no resolution) and its docstring, plus (for a method) which class it belongs to.
- `ContentType` values reuse `symbol_doc`/`package_doc` directly, matching NORM-008's own reasoning — one shared shape across every ecosystem's structured content, not per-ecosystem types.
- Docstring *style* (Google/NumPy/Sphinx-style sections) is stored as raw text, not parsed into structured param/return fields in v1 — the signature's own type annotations already carry that signal when present; deeper docstring-section parsing is a natural, separable follow-up, not blocking this ticket.

### What counts as "exported" (deliberately narrower than it first looks)
Non-`_`-prefixed names and an explicit `__all__` are the easy, statically-certain cases — but `__all__` can be built dynamically (list concatenation, a loop, an import-time computation), and a package's real public API is often re-exported through `__init__.py` (`from .core import Thing`), not defined where it's used. Getting this wrong silently either hides real public symbols or fabricates a "complete" list that isn't. v1's actual rule: **extract every statically-resolvable export and simple re-export** (a literal `__all__` list of string constants; a direct `from .module import name` re-export chain resolvable within the package's own tree) **and explicitly record whatever isn't statically resolvable as a coverage limitation** on the module's own object (e.g. `Metadata["exports_dynamic"] = "true"`), rather than silently presenting a partial symbol list as if it were the package's complete public surface. An agent — or a person — reading the result can then tell "this is everything" from "this package computes its exports dynamically, treat this list as partial."

### Combining `.pyi` and `.py` sources for one module
A `.pyi` stub's signature is authoritative when both exist (that's specifically what stub files are for), but stubs commonly carry no docstrings at all — discarding the real `.py` source's docstrings in that case throws away real, useful evidence for no reason. v1: prefer the `.pyi` signature, but fall back to the corresponding `.py` source's docstring for the same symbol when the stub's own is empty.

### Sparse-checkout scoping
`normalize.SparsePatterns(domain.EcosystemPython)` gains `*.py` (mirroring `*.go`'s own precedent) and `*.pyi` (stub files — pure declarations, cheapest to parse when present, checked before falling back to the real `.py` source for a given module).

## Non-goals
- No import/execution of the target package under any circumstance — this is the ticket's central constraint, not a v1 shortcut to revisit later.
- No docstring-style-aware structured parsing (Google/NumPy/Sphinx sections split into fields) in v1 — raw docstring text only.
- No handling of C-extension-backed packages (no Python source to statically parse at all) beyond producing zero structured objects for them, same as any package with no source available.
- No cross-module type resolution — an annotation referencing a type from another module is rendered as written, unresolved.

## Tests

**Correctness is the acceptance gate, not the `structured=true` count.** A parser that emits one wrong symbol per package would still move the count off zero; that proves nothing about whether the content is right. The count is a coverage metric, checked after correctness, not instead of it.

- **A golden fixture package**, hand-built: a module with a real docstring, an exported function with a documented signature, an exported class with a documented method, a private (`_`-prefixed) symbol that must NOT appear, a name re-exported through `__init__.py` that must resolve to its real definition, a dynamically-built `__all__` (or equivalent) that must be recorded as a coverage limitation rather than silently omitted or fabricated, and a sibling package in the same worktree whose symbols must never leak in. Asserts exact signature/docstring text and exact object count, not just "something structured was produced."
- A `.pyi`+`.py` pair for the same symbol: the `.pyi` signature wins, but the `.py` source's docstring is kept when the stub's own is empty.
- A real, pure-Python PyPI package produces per-symbol objects with real signatures and docstrings, statically, with the target package never imported (verified: the test environment need not even have the package's own dependencies installed for extraction to succeed).
- A C-extension-only package (no parseable Python source) produces zero structured objects, not a failure.
- `python3` unavailable on the host: sync still succeeds, structured extraction is silently skipped (logged, not fatal).
- Live: re-run `hack/verify-sync` against mem0's Python dependencies — depends on REG-013 already being live (see this ticket's own Depends-on note) — record `structured=true` count (baseline: 0), reported alongside the golden-fixture correctness result, never in place of it.

## Acceptance criteria
- [ ] The golden fixture's exact signatures/docstrings/object count are extracted correctly; its private symbol is excluded; its `__init__.py` re-export resolves; its dynamic export is recorded as a limitation, not silently dropped or fabricated; its sibling package's symbols never leak in.
- [ ] A real PyPI package produces real per-symbol structured objects, with zero execution of its code.
- [ ] `.pyi` stubs' signatures are preferred over source when both exist, with `.py` docstrings retained when the stub has none.
- [ ] Missing `python3` degrades gracefully, never fails a sync.
- [ ] `hack/verify-sync`'s `structured=true` count rises from 0 on a real mem0 re-run.
