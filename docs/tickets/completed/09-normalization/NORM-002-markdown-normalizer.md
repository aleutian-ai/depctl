# NORM-002: Markdown normalizer

**Epic:** Normalization Pipeline
**Status:** done
**Depends on:** NORM-001
**Estimated size:** medium

## Goal
Parse `.md` (and plain `.mdx`) files into structured `KnowledgeObject`s, extracting title, heading hierarchy, code fences, prose blocks, and links, while preserving source path and heading for provenance.

## Non-goals
- Does not perform embeddings or semantic analysis — pure syntactic extraction.
- Does not render HTML or execute MDX components — treat `.mdx` as plain Markdown with JSX-looking blocks passed through as opaque text; skip `.mdx` files that fail to parse as Markdown rather than trying to be clever.

## Simplicity constraints
- Use a single well-established Go Markdown parser (e.g. `goldmark`) rather than hand-rolling a parser. Pick one library and do not special-case every Markdown dialect extension.
- One `KnowledgeObject` per heading section is a chunking concern (CHUNK-002), not this ticket's job — NORM-002 just needs to preserve heading hierarchy in metadata so chunking can use it later. It's acceptable for this ticket to emit one `KnowledgeObject` per document with structured metadata, deferring section-splitting to chunking.

## Design
- Package: `internal/normalize/markdown`
- `MarkdownNormalizer` implements `Normalizer` (NORM-001): `Name() == "markdown-normalizer"`, `Version() == "v1"`.
- `Supports(src)`: true when `src.LogicalPath` ends in `.md` or `.mdx`.
- Extraction via AST walk (e.g. goldmark AST):
  - Title: first H1, or filename fallback.
  - Heading hierarchy: recorded as a path per block, e.g. `["Getting Started", "Installation"]`, stored in `KnowledgeObject.Heading` / `Metadata`.
  - Code fences: preserved with language tag in metadata (`content_type=code`, `language=go`).
  - Prose blocks and links: preserved as-is in `Content`; links extracted into `Metadata["links"]` (comma-joined or JSON array string) for provenance, not rewritten.
- `KnowledgeObject.LogicalPath` = source-relative path; `Language` = detected code language if the object represents a fenced block, else empty.

## Inputs / Outputs
- Input: `SourceSnapshot` pointing to a materialized `.md`/`.mdx` file.
- Output: `[]domain.KnowledgeObject` (structure per Design; ID assignment happens in HASH-002, not here).

## Failure behavior
- Malformed Markdown: goldmark is lenient by design and rarely hard-fails; if parsing produces zero usable content, return an object with raw content and a `Metadata["parse_warning"]="true"` flag rather than erroring the whole file out.

## Tests
- README fixture (`testdata/sources/markdown/`) — title + top-level headings extracted correctly.
- Nested headings fixture — heading hierarchy correctly nested 2–3 levels deep.
- Fenced Go and Python code blocks — language tags correctly captured.
- Golden snapshot test comparing normalized output to `testdata/golden/` fixture (per plan §44, golden tests guard against silent parser drift).

## Acceptance criteria
- [x] README fixture normalizes correctly.
- [x] Nested headings preserved in metadata.
- [x] Fenced Go/Python code blocks extracted with correct language tags.
- [x] Golden snapshot test passes and is wired into CI.

## Post-implementation fix (found by real-world normalization)

Running the normalizer against a real README (`spf13/cobra`'s, via `hack/normalize-preview`, `~/offline-knowledge`) surfaced a title-detection bug the fixture tests didn't catch: the document's `Title` came back as `"Warp, the AI terminal for devs"` — a sponsor callout rendered as an `### H3` inside an HTML `<div>` block, appearing before the doc's real `# Overview` heading — instead of `"Overview"`.

Root cause: the heading-level clamp that keeps an out-of-order heading (e.g. an H3 appearing before any H1/H2) from indexing out of range on the breadcrumb stack was reused, unmodified, for title detection too. The clamp legitimately treats a stray first-seen H3 as "one level deep" for breadcrumb purposes, but that clamped value was also what the `level == 1` title check compared against — so any out-of-order heading that happened to be first in the document got silently promoted to "the title," regardless of its real level in the source.

Fixed in `internal/normalize/markdown/normalize.go`: title detection now checks the heading's real, unclamped `h.Level == 1` — only a genuine `# ...` can ever become the title. The breadcrumb stack still uses the clamped value, since that behavior (best-effort nesting for malformed documents, not an error) is still correct for that purpose. Regression test: `TestHeadingBeforeH1DoesNotStealTitle` (`testdata/sources/markdown/skewed-heading.md`), reproducing the exact cobra README shape.
