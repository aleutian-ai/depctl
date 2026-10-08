# Epic: Curated Text Acquisition

A dependency's registered knowledge is not limited to its source repository or official docs. The scratch doc's §6A ("Multi-text source mappings and curated text ingestion") makes the case for letting a curator attach additional text sources — selected documentation pages, internal wiki/runbook pages, technical articles — to a dependency entry, on top of what epic 07 (knowledge registry) and epic 24 (website acquisition) already support. The governing principle throughout this epic is:

> **Curators select the corpus; depctl acquires and indexes it.** No source in scope here is ever discovered by crawling — every URL/page ID an object here indexes was named explicitly, in the manifest, by a human.

## Relationship to epic 24 (website-acquisition)

Epic 24 already specs the pieces this epic depends on and does not duplicate:
- `HTTP-001-http-acquisition-client.md` — the shared HTTP client (timeouts, conditional requests, rate limits, size limits). `HTTP-004` here reuses it as-is.
- `HTTP-003-html-normalizer.md` — HTML → `KnowledgeObject` extraction. `HTTP-004` here reuses it as-is; no new normalizer.

**Open decision, flagged here rather than silently resolved:** epic 24's `HTTP-002-sitemap-reader.md` specs sitemap-driven *discovery* of page URLs — feed it a `sitemap.xml`, get back a list of candidate pages, filtered by allow/deny glob rules. That shape is in tension with this epic's "no broad recursive spidering, ever" principle: HTTP-002 as currently specced could plausibly be pointed at a manifest source and used to auto-ingest every page a sitemap lists, which is exactly what §6A's "Website and wiki ingestion should be explicitly curated, not broadly spidered" section rules out for automatic ingestion. This epic does not rewrite HTTP-002 — that ticket belongs to epic 24 and its own maintainer's judgment — but flags the tension as an open decision for whoever picks up HTTP-002: it should either (a) be explicitly scoped down to a curator-facing *discovery aid* (`depctl registry suggest-pages <sitemap-url>` or similar — proposes URLs for a human to copy into a manifest, never auto-ingests) or (b) be left as general sitemap-reading infrastructure that HTTP-004 below simply never calls automatically. Either resolution is fine; what's not fine is a code path that turns a `sitemap.xml` URL directly into indexed content with no curator step in between.

## Tickets
- [HTTP-004](HTTP-004-explicit-curated-page-list.md) — registry `Source` type for an explicit list of curator-picked page URLs (or a `collection` grouping many sources under one dependency), reusing epic 24's HTTP client and HTML normalizer. No recursive link-following, ever.
- [CONF-001](CONF-001-confluence-wiki-acquisition.md) — Confluence/internal-wiki as another acquisition provider, same acquisition contract shape as epic 24's HTTP client (fetch → normalize → attribute), not a new retrieval architecture. Explicit page IDs only, user-provided auth via config/env-var reference (never embedded in registry YAML).

## Non-goals for this epic
- No crawling, no sitemap-driven auto-ingestion, no "index this whole wiki space" mode — every page indexed here was named explicitly by a curator, in every ticket.
- No video/audio/ASR acquisition (scratch doc §6A: explicitly out of scope; text-first only).
- No query-time source-precedence routing (scratch doc §6A "Source precedence should be query-sensitive") — that's a query-serving concern, not an acquisition one, and is evidence-gated per §3.5 rather than built here.
- No `extend`/`replace` manifest-layering semantics (scratch doc §6A "Registry layering") — this epic only adds a new `Source.Type`/`collection` shape to the existing single-layer manifest format; multi-layer registry composition (Aleutian → org → project-local) is a separate, larger change to `internal/registry` not scoped here.
