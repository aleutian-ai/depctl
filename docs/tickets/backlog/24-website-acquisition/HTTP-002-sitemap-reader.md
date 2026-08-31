# HTTP-002: Sitemap reader

**Epic:** Documentation Website Acquisition
**Status:** planned
**Depends on:** HTTP-001
**Estimated size:** small

## Goal
Discover documentation page URLs by reading `sitemap.xml` (including sitemap index files), applying registry-configured path allow/deny rules.

## Non-goals
- Crawling a site without a sitemap (out of scope for v1 — if no sitemap, the registry manifest source is simply skipped/warned).
- JavaScript-rendered sitemaps.

## Simplicity constraints
- Use `encoding/xml` with minimal structs for `<urlset>` and `<sitemapindex>` — no external sitemap library.
- Allow/deny rules are a simple ordered list of glob or prefix patterns from the registry manifest; do not build a general rule-engine.

## Design
Package: `internal/source/http` (file `sitemap.go`).

```go
type SitemapReader struct {
    Client *Client // from HTTP-001
}

func (s *SitemapReader) Discover(ctx context.Context, sitemapURL string, allow, deny []string) ([]string, error)
```
If the fetched document is a `<sitemapindex>`, recurse into each child sitemap (bounded depth, e.g. max 2 levels, to avoid pathological loops). Apply `allow`/`deny` glob patterns (`path.Match` semantics) to each discovered URL path; deny takes precedence.

## Inputs / Outputs
- Input: sitemap URL, allow/deny path patterns from the registry manifest (§18 registry manifest format — `sources[].path rules`, if defined there; otherwise from normalizer hints).
- Output: ordered list of candidate page URLs.

## Failure behavior
- Sitemap unreachable → typed error, source acquisition for that `KnowledgeSource` marked failed (not fatal to the whole sync).
- Malformed XML → typed error with URL context.
- Recursion depth exceeded → stop recursing, log a warning, return what was collected so far.

## Tests
- Flat `<urlset>` sitemap → all URLs returned.
- `<sitemapindex>` with two child sitemaps → URLs merged from both.
- Deny pattern excludes matching paths; allow-list restricts to only matching paths when set.
- Malformed XML returns a clear error.

## Acceptance criteria
- [ ] Sitemap index recursion terminates and returns the expected URL set for a fixture.
- [ ] Allow/deny rules from a registry manifest are honored.
