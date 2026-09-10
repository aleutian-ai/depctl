# NORM-004: Go source documentation normalizer

**Epic:** Normalization Pipeline
**Status:** done
**Depends on:** NORM-001
**Estimated size:** medium

## Goal
Extract package docs, exported types/interfaces/functions/methods/constants, doc comments, and signatures from Go source using the standard library's parser/doc tooling — not full code search, just API-reference-shaped documentation.

## Non-goals
- Does not index unexported symbols or full implementation bodies by default (per plan: "This project is primarily dependency-documentation lifecycle, not code search").
- Does not build a general code-search index.

## Simplicity constraints
- Use only `go/parser`, `go/doc`, `go/ast` from the standard library — no external Go static-analysis dependency.
- Extract examples only where `go/doc`'s `Examples` field already surfaces them trivially (from `_test.go` `Example*` functions in the same package) — do not build custom example-detection heuristics.

## Design
- Package: `internal/normalize/godoc`
- `GoDocNormalizer` implements `Normalizer`: `Name() == "godoc-normalizer"`, `Version() == "v1"`.
- `Supports(src)`: true when the `SourceSnapshot` points at a directory containing `.go` files (package directory), not a single file — Go doc extraction operates per-package.
- Implementation:
  1. `parser.ParseDir` the package directory.
  2. `doc.New(pkg, importPath, doc.AllDecls)` to get a `*doc.Package`.
  3. Emit one `KnowledgeObject` for the package doc comment (`ContentType="package_doc"`).
  4. Emit one `KnowledgeObject` per exported type/interface/function/method/constant (`ContentType="symbol_doc"`), each with `Metadata["symbol"]`, `Metadata["signature"]` (rendered via `go/printer` or `types.ExprString` on the decl), and the doc comment as `Content`.
  5. Only exported (capitalized) identifiers are processed — `doc.AllDecls` combined with a manual exported-name filter, or rely on `go/doc`'s default exported-only behavior (do not pass `doc.AllDecls` if it would pull in unexported — verify against stdlib default, which already filters to exported by default; `AllDecls` only affects whether *all* declarations of a type are shown vs. one, not export filtering).

## Inputs / Outputs
- Input: `SourceSnapshot` pointing to a Go package directory (typically from a GIT-002 materialized worktree).
- Output: `[]domain.KnowledgeObject`, one for package docs + one per exported symbol.

## Failure behavior
- Package fails to parse (syntax error, build-tag-excluded files, etc.): return a typed error listing the failing file; caller (GEN-002) tracks this as part of "parser error rate" for VAL-002 sanity thresholds, not necessarily fatal to the whole generation.

## Tests
- Exported function lookup: fixture package with a documented exported func produces a correct symbol object.
- Type/method docs: fixture with an exported type + methods produces correctly linked symbol objects (method associated with its receiver type in metadata).
- Package comment: fixture with a `// Package foo ...` doc comment produces the package_doc object.
- Golden snapshot test in `testdata/golden/` for a representative fixture package.

## Acceptance criteria
- [x] Exported function lookup works against fixture.
- [x] Type/method docs correctly extracted and linked to their type.
- [x] Package doc comment captured.
- [x] Unexported symbols never appear in output.
