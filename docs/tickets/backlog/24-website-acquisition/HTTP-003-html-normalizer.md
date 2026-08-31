# HTTP-003: HTML normalizer

**Epic:** Documentation Website Acquisition
**Status:** planned
**Depends on:** NORM-001 (Normalizer interface), HTTP-001
**Estimated size:** medium

## Goal
Extract title, main content, heading structure, code blocks, and canonical URL from fetched HTML documentation pages, producing `KnowledgeObject`s via the standard `Normalizer` interface.

## Non-goals
- Browser rendering of dynamic/JS-driven sites — explicitly out of scope; an external crawler adapter handles those.
- General-purpose readability/boilerplate-removal ML — a heuristic approach is sufficient.

## Simplicity constraints
- Use `golang.org/x/net/html` (standard extended library) for parsing; do not add a heavier HTML-to-Markdown conversion dependency unless the hand-rolled extraction proves inadequate on real doc sites.
- Main-content extraction can use simple heuristics (prefer `<main>`, `<article>`, or the largest text-bearing block) — do not build a general Readability-clone algorithm.

## Design
Package: `internal/normalize/html`

```go
type Normalizer struct{}

func (n *Normalizer) Name() string { return "html" }
func (n *Normalizer) Version() string { return "v1" }
func (n *Normalizer) Supports(src domain.SourceSnapshot) bool // content-type text/html
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error)
```
Extraction targets per page: `<title>`, canonical `<link rel="canonical">` (fallback to source URL), heading hierarchy (`h1`-`h4`) used to build `Heading` paths, `<pre><code>` blocks preserved verbatim with a best-effort language hint from `class="language-*"`.

## Inputs / Outputs
- Input: `SourceSnapshot` with raw HTML bytes + source URI.
- Output: `[]domain.KnowledgeObject` (one or more per page, matching the Markdown normalizer's shape so downstream chunking/fingerprinting is uniform).

## Failure behavior
- Unparseable HTML → typed `NormalizeError`, that page skipped, parser-error-rate counter incremented (feeds VAL-002 sanity thresholds).
- Empty extracted content (e.g. pure JS shell) → skipped with a warning, not treated as fatal.

## Tests
- Simple static doc page fixture: title/headings/code blocks extracted correctly.
- Page with `<main>` wrapper vs page without (fallback heuristic).
- Canonical URL present vs absent.
- Malformed HTML input does not panic and produces a typed error.

## Acceptance criteria
- [ ] Golden-snapshot test against a committed HTML fixture under `testdata/sources/html/`.
- [ ] Parser error rate is tracked and exposed for VAL-002 sanity thresholds.
