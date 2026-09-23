# Epic: JS/TS and Python structured API normalizers

**Found live, this session:** the ecosystem-coverage gap `docs/architecture.md` now documents explicitly — every Node/Python sync that succeeds today (epic 56 closed the acquisition half) still only ever produces README/CHANGELOG/LICENSE text. Neither ecosystem has anything like `godoc` (NORM-004): real, per-symbol, signature-level API documentation extracted directly from what the package ships.

This epic closes that, following the same pattern `docs/tickets/backlog/40-structured-artifact-normalizers` already established for OpenAPI/Protobuf: a narrow, format-specific `normalize.Normalizer` per ecosystem, one `KnowledgeObject` per exported symbol, registered into the existing `normalize.Registry` selection mechanism — not a general code-intelligence platform.

## Why declarations, not full compilation or execution

`godoc` works entirely off `go/parser`/`go/doc` — stdlib, static, no code execution, no external dependency. Neither JS/TS nor Python has an equivalent that's *also* purely static-in-Go, but both ecosystems have a static-and-declarative middle ground that gets most of the value at a fraction of the cost and risk of the alternatives:

- **`.d.ts` declaration files** — many published npm packages already ship these (bundled, or via `@types/*`). They're pure type/signature declarations with no implementation — exactly the "public API surface" shape `godoc` extracts, sitting in the package for free when present.
- **JSDoc comment blocks** — for the (large) share of npm packages with no `.d.ts` at all, JSDoc-style comments above exported functions carry the same signature/param/return information, extractable via a syntax-level parse, no type-checking needed.
- **griffe's approach for Python** — static `ast`-module analysis, never imports or executes the target module. This is the deliberate alternative to Sphinx/autodoc/pydoc, which import and run arbitrary third-party code to introspect it — already ruled out earlier this session as incompatible with syncing a private or unfamiliar dependency safely.

## The real tradeoff this epic accepts, explicitly

Unlike `godoc`, **none of these run as pure, dependency-free Go.** Every real option means either a `python3`/`node` subprocess (a genuinely new operational requirement — ragctl's only current acquisition-time runtime dependency is the `git` binary) or CGo tree-sitter bindings (a build-complexity tradeoff already deferred once elsewhere, `docs/tickets/backlog/25-additional-vector-backends/VEC-015-sqlite-embedded-backend.md`). This epic's tickets each make that call explicitly rather than default into it — see each ticket's own Design section.

## Tickets
- [NORM-008](NORM-008-typescript-declaration-normalizer.md) — **done** (`.d.ts` path; JSDoc fallback deferred — see its own post-implementation note).
- [NORM-009](NORM-009-python-static-signature-normalizer.md) — **done**, exactly as designed.

## Non-goals (this epic)
- No full TypeScript compiler integration (TypeDoc/`api-extractor`) — those need a valid, buildable `tsconfig` and are designed for a package's own build process, not parsing an arbitrary third-party npm package after the fact.
- No Sphinx/autodoc/pydoc-style extraction — those import and execute the target module, which is unacceptable for a private or unfamiliar dependency ragctl doesn't control.
- No semantic type-checking or cross-file type resolution for either ecosystem — signature text is extracted and stored as-is, the same "syntactic, not semantic" scope `godoc` itself has.
- No universal AST/tree-sitter platform spanning every language — matches `docs/tickets/backlog/40-structured-artifact-normalizers`' own explicit non-goal for the same reason.
- No change to acquisition/sparse-checkout scoping (epic 56) — this epic is purely about what gets extracted from an already-correctly-scoped worktree, feeding `normalize.SparsePatterns` new file patterns to fetch, not changing how `Subdir`/monorepo boundaries are determined.

## Acceptance criteria

**Correctness first, coverage count second** — each ticket's own golden-fixture test (known symbols, a deliberately-excluded private one, a sibling package that must never leak in) is the real bar; a `hack/verify-sync` `structured=true` count rising from zero is a supporting coverage metric, never the acceptance criterion on its own. See each ticket's own Tests section.

- [x] NORM-008's golden fixture passes: correct signatures/docs, private symbol excluded, sibling package doesn't leak.
- [x] A real npm package that ships `.d.ts` files produces per-symbol `KnowledgeObject`s with real exported signatures — verified live (`commander.js`, 110 objects, real hand-authored `.d.ts` including a multi-line wrapped signature).
- [ ] **Deferred, not built** — a real npm package with no `.d.ts` but real JSDoc comments producing structured objects via a JSDoc path. See NORM-008's own post-implementation note for why (a real two-phase-acquisition problem, not a shortcut).
- [x] NORM-008's runtime dependency is reproducible — the actual answer ended up stronger than "pinned": the embedded `extract.js` needs no npm package at all, only `node` itself, so there is nothing to pin.
- [x] NORM-009's golden fixture passes: correct signatures/docstrings, private symbol excluded, `__init__.py` re-export resolved, a dynamic export recorded as a limitation not fabricated.
- [x] A real, uncurated PyPI package produces per-symbol `KnowledgeObject`s with real exported signatures, with zero imports/execution of the target package's code — verified live (`requests` 2.32.3, 65 objects, including resolved re-exports, never `pip install`-ed).
- [ ] **Not yet done** — re-run `hack/verify-sync` against a real mem0 sync with both normalizers active and record the before/after `structured=true` count. The live single-package verifications above prove the normalizers work; a full mem0 re-run would prove real-world coverage at scale. Natural next action, not blocking this epic's core scope.
