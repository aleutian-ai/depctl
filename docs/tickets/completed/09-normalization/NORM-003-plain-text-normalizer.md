# NORM-003: Plain text normalizer

**Epic:** Normalization Pipeline
**Status:** done
**Depends on:** NORM-001
**Estimated size:** small

## Goal
Handle `.txt` files, `.rst` as a plain-text fallback (no real reStructuredText parsing), release-notes-shaped text files, and LICENSE-like documents when explicitly configured, producing one `KnowledgeObject` per file.

## Non-goals
- Does not implement a reStructuredText parser — `.rst` is treated as plain text in v0.1.
- Does not classify `content_type=release_note` — that's NORM-005, which builds on this normalizer's output for `.txt`-shaped release notes.

## Simplicity constraints
- No structure extraction beyond trimming and storing raw content — this is intentionally the simplest normalizer. Do not add heuristic section-splitting here.
- LICENSE handling is opt-in via config (a normalizer-hint flag from the registry manifest or project config), defaulting to skipped, since license text is rarely useful retrieval content and bloats the corpus.

## Design
- Package: `internal/normalize/plaintext`
- `PlainTextNormalizer` implements `Normalizer`: `Name() == "plaintext-normalizer"`, `Version() == "v1"`.
- `Supports(src)`: true for `.txt`, `.rst`; true for files matching `LICENSE*` only if a `Metadata["include_license"]="true"` hint is present on the `SourceSnapshot` (set upstream by acquisition based on manifest/config).
- `Normalize`: read file, produce a single `KnowledgeObject` with `Content` = trimmed raw bytes, `Title` = filename, `ContentType` = `"text"`.

## Inputs / Outputs
- Input: `SourceSnapshot` for a `.txt`/`.rst`/opt-in `LICENSE*` file.
- Output: `[]domain.KnowledgeObject` with exactly one element.

## Failure behavior
- Non-UTF8 content: replace invalid sequences (`golang.org/x/text/encoding` or simple `strings.ToValidUTF8`) rather than failing the whole file — log a warning via metadata flag.

## Tests
- `.txt` file normalizes to one object with correct title/content.
- `.rst` file normalizes as plain text (no RST directive parsing attempted).
- LICENSE file skipped by default; included when hint flag set.

## Acceptance criteria
- [x] `.txt` and `.rst` fixtures normalize correctly as plain text.
- [x] LICENSE-like files are excluded unless explicitly configured.
