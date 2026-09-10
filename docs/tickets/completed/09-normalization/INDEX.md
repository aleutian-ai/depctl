# Epic: Normalization Pipeline

Converts acquired raw content (Markdown, plain text, Go source, release notes) into the domain's structured `KnowledgeObject` model. All normalizers share one interface (NORM-001) and are versioned so that parser/chunker changes can trigger deliberate re-indexing rather than silent staleness. This project is a dependency-documentation lifecycle tool, not a code search engine — normalizers extract API-reference-shaped content, not full implementation bodies.

## Tickets
- [NORM-001](NORM-001-normalizer-interface.md) — Shared `Normalizer` interface and registry/selection helper.
- [NORM-002](NORM-002-markdown-normalizer.md) — Markdown/MDX parsing: title, headings, code fences, prose, links.
- [NORM-003](NORM-003-plain-text-normalizer.md) — Plain text/RST-as-text/opt-in LICENSE handling.
- [NORM-004](NORM-004-go-doc-normalizer.md) — Go package/exported-symbol documentation via `go/doc`.
- [NORM-005](NORM-005-release-note-normalizer.md) — Tags and version-sections release notes on top of NORM-002/003.
