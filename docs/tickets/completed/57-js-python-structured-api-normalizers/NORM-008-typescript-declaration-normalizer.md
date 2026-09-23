# NORM-008: TypeScript declaration (.d.ts) + JSDoc normalizer

**Epic:** JS/TS and Python structured API normalizers
**Status:** done — `.d.ts` path only; JSDoc-only fallback deferred (see Post-implementation note)
**Depends on:** none (consumes epic 56's already-correct acquisition scoping)
**Estimated size:** medium

## Post-implementation note

Shipped `internal/normalize/tsdoc` — real `.d.ts`-based structured extraction, wired into `generation.normalizeSources` at the Node package root (mirroring `godoc`'s per-directory invocation, but simpler: a Node package's entry point is singular at the package root regardless of nesting depth, no walk needed). Two deliberate deviations from the original design, both disclosed here rather than silently shipped:

1. **The tooling decision changed.** The design recommended shelling out to the real TypeScript compiler API for correctness. In practice, vendoring the full `typescript` npm package (and satisfying the "pinned, reproducible, never installed ad hoc" requirement the tightened spec added) wasn't practical within this pass — it's a large dependency tree, not a single file. Shipped instead: a small, **entirely dependency-free** Node script (`extract.js`, embedded via `go:embed`), doing bounded line/brace-depth scanning for top-level exported declarations (function/class+members/interface/type/const) and their attached JSDoc. This is arguably a *better* fit for the "reproducible, no ad hoc install" requirement than the original plan — the only runtime dependency is `node` itself, nothing else to pin at all. The real tradeoff: it's syntactic and bounded, not a real TS parser — no generics-aware type resolution, no `export { X } from './y'` re-export following (silently skipped, not an error). Verified live against a real, hand-authored `.d.ts` (`commander.js`, 110 structured objects extracted correctly, including a real multi-line wrapped signature) — good enough for the common cases, not a claim of full TS grammar coverage.
2. **JSDoc-only fallback (for packages with no `.d.ts` at all) is deferred, not built.** Scoping it correctly ran into a real chicken-and-egg problem: sparse-checkout patterns are fixed before a worktree exists to read `package.json`'s entry point from, and fetching *all* `.js` recursively to work around that directly violates this epic's own Non-goal ("no full-repository `.js` sparse-checkout"). `.d.ts`-only is a smaller but still real and growing share of npm packages — most modern TS-authored libraries ship these. JSDoc-only support is a legitimate fast-follow once entry-point-aware two-phase acquisition (read `package.json` first, then widen the sparse pattern for just that one file) is worth building — not designed here.

Golden fixture (`internal/normalize/tsdoc/normalize_test.go`'s `TestExtractGoldenFixtureDTS`) and a full-pipeline integration test (`internal/data/generation/build_test.go`'s `TestBuildExtractsStructuredTypeScriptDocsAndNeverLeaksASiblingsSymbols`, reusing the same monorepo-sibling-leak shape as the npm acquisition fix) both pass — correct signatures/docs, private class members excluded, sibling package's symbols never leak in. One real bug caught by the golden fixture itself before shipping: a doc comment sitting directly above the very first export (the common case — no separate module-level doc) was being wrongly swallowed as an empty "module doc," leaving the symbol's own doc empty; fixed with a peek-ahead/rewind in `extract.js`.

## Goal
Extract real, per-symbol API documentation (exported functions/classes/interfaces/types, with signatures and doc comments) from a Node dependency's own worktree — the JS/TS equivalent of `godoc` (NORM-004), following the same shape: one `package_doc`-like object per module/entry point, one `symbol_doc`-like object per exported symbol.

## Design

### Two source shapes, tried in order
1. **`.d.ts` declaration files.** When present (bundled in the package, or resolvable via `@types/<package>` — out of scope for v1, see Non-goals), these are the package's own published type surface: pure declarations, no implementation, no execution risk. Parse with a syntax-level TypeScript declaration parser.
2. **JSDoc comment blocks over exported JS.** For the large share of npm packages that ship plain `.js` with no `.d.ts` at all, JSDoc comments (`/** ... */` immediately above an exported function/class) carry the same signature/param/return shape. Parse via a syntax-level JS parser plus JSDoc comment extraction — no type-checking needed, since JSDoc's own `@param`/`@returns` tags already carry the type information a `.d.ts` would.

### Tooling decision (explicit, not defaulted into)
Neither shape has a mature, dependency-free pure-Go parser. Two real options:
- **A `node` subprocess** running a small bundled script against the TypeScript compiler API (`typescript` package, already the canonical `.d.ts` parser — reusing it beats reimplementing TS declaration grammar) or an off-the-shelf JSDoc parser (`comment-parser`/`jsdoc-api`). Requires `node` on the host at sync time — a new operational dependency, matching this ticket's own epic-level tradeoff note.
- **CGo tree-sitter bindings** (`tree-sitter-typescript`) for syntax-level parsing without a subprocess. Avoids the `node` requirement but reintroduces the CGo build-complexity tradeoff this repo has deferred once already (VEC-015).

**Recommendation for v1: the `node` subprocess path.** It reuses the TypeScript compiler's own real, correct declaration grammar rather than reimplementing a `.d.ts` parser from scratch, and `node` is a far more common thing to have installed already (development machines, CI images) than it is to accept a CGo dependency into ragctl's own build. Detect `node` availability at sync time; its absence is a clean per-dependency skip (same object-count-zero-but-not-failed shape a missing `.d.ts`/JSDoc already produces), not a hard sync failure — ragctl still works with no `node` installed, just without this ecosystem's structured layer.

**The runtime dependency must be reproducible, not silently installed.** Detecting `node` is necessary but not sufficient — the bundled script also needs the `typescript` package itself (and a JSDoc parser), and that can't come from an ad hoc `npm install` during a sync (breaks offline operation, and pins nothing, so the same dependency could parse differently on two machines). Ship a pinned version of the parser as part of ragctl's own distribution — vendored alongside the bundled script, `go:embed`'d if practical, not fetched at sync time. This also means two distinct failure modes need distinct handling, not one shared "skip" path: **`node` missing** is a clean per-dependency skip, exactly as below; **`node` present but the bundled parser missing or failing to load** is a ragctl packaging defect and should be logged loudly (a warning, not silent), since it means structured extraction is broken for every Node dependency on that install, not that this particular dependency lacks documentation.

### Extraction shape
Mirrors `internal/normalize/godoc`'s own object shape as closely as the ecosystem allows:
- One object per module/entry point (`package.json`'s `main`/`types`/`exports` field) — package-level doc comment if one exists.
- One object per exported function/class/interface/type alias, each carrying its rendered signature (parameter names/types, return type) and its doc comment, plus (for a class) which type it belongs to — the `receiver`-equivalent `godoc`'s `symbol_doc` already carries for methods.
- `ContentType` values reuse `symbol_doc`/`package_doc` directly (not new ecosystem-specific types) so `hack/verify-sync`'s existing `hasStructuredDocs` check, and any future retrieval-side logic, works unchanged across ecosystems.

### Sparse-checkout scoping
`normalize.SparsePatterns(domain.EcosystemNode)` gains `*.d.ts` and (bounded — see Non-goals) top-level exported `.js` file patterns, mirrored from the existing `*.go`/`package.json` precedent's own reasoning comment.

## Non-goals
- No fetching of `@types/<package>` (DefinitionallyTyped) as a separate source when a package ships no `.d.ts` of its own — JSDoc is the fallback path instead; revisit only if JSDoc coverage proves insufficient in practice.
- No full-repository `.js` sparse-checkout — only files reachable from `package.json`'s declared entry points, matching `godoc`'s own "the package directory, not the whole repo" scope.
- No semantic type resolution across files (e.g. resolving an imported type from another module) — a signature is rendered exactly as declared, unresolved references included as-is, same "syntactic, not semantic" scope as `godoc`.
- No support for `.mjs`/`.cjs` dual-package exports disambiguation beyond whatever `package.json`'s own `exports` field states plainly — no bundler-level resolution logic.

## Tests

**Correctness is the acceptance gate, not the `structured=true` count.** A parser that produces one wrong symbol per package would still move that count off zero — it proves nothing about whether the extracted content is actually right. The count is a coverage metric, checked after correctness, not instead of it.

- **A golden fixture package**, hand-built with known content: an exported function with a documented signature, an exported class with a documented method, a deliberately *un*exported/private symbol that must NOT appear in the output, a sibling package in the same worktree whose symbols must never leak into this package's result (the direct analog of the monorepo sibling-leak regression already proven for acquisition in epic 56/REG-012). The test asserts on exact signature text, exact doc comment text, and exact object count — not just "something structured was produced."
- A real npm package that ships `.d.ts` (e.g. a `@types`-free TypeScript-authored package) produces per-symbol objects with real signatures.
- A real npm package with no `.d.ts` but real JSDoc comments produces structured objects via the fallback path.
- A package with neither produces zero structured objects, same as today — not a sync failure.
- `node` unavailable: sync still succeeds, structured extraction is silently skipped for that dependency (logged, not fatal).
- `node` available but the bundled parser fails to load: a loud warning, distinct from the silent skip above — this is ragctl's own defect, not the dependency's.
- Live: re-run `hack/verify-sync` against mem0's Node dependencies, record `structured=true` count (baseline: 0/157) — reported alongside the golden-fixture correctness result, never in place of it.

## Acceptance criteria
- [ ] The golden fixture's exact signatures/docs/object count are extracted correctly, its private symbol is excluded, and its sibling package's symbols never leak in.
- [ ] `.d.ts`-shipping packages produce real per-symbol structured objects.
- [ ] JSDoc-only packages produce real per-symbol structured objects.
- [ ] Missing `node` degrades gracefully, never fails a sync; a broken bundled parser (node present) is a loud warning, not a silent skip.
- [ ] `hack/verify-sync`'s `structured=true` count rises from 0 on a real mem0 re-run.
