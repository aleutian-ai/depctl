# Epic: Documentation Website Acquisition

Acquire and normalize static documentation websites as a fallback/supplement to Git-source documentation. Ship only after Git-source ingestion (GIT-001/002/003) is working — Git/source Markdown is always preferred over scraped HTML when available. Explicitly avoids browser rendering of dynamic sites; that's delegated to an external crawler adapter if ever needed.

- [HTTP-001](HTTP-001-http-acquisition-client.md) — shared HTTP client: timeouts, conditional requests, rate limits, size limits, redirect policy.
- [HTTP-002](HTTP-002-sitemap-reader.md) — discover page URLs via `sitemap.xml`/sitemap index, with allow/deny path rules.
- [HTTP-003](HTTP-003-html-normalizer.md) — extract title/headings/code/canonical URL from fetched HTML into `KnowledgeObject`s.
