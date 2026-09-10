# NORM-005: Release-note normalizer

**Epic:** Normalization Pipeline
**Status:** done
**Depends on:** NORM-002, NORM-003
**Estimated size:** small

## Goal
Tag content from known release-note sources (e.g. `CHANGELOG.md`, `RELEASES.md`, GitHub Releases API output) with `content_type = release_note` and, where possible, preserve per-release heading/version sections so `get_release_changes` (MCP-003, later) can answer "what changed between version X and Y".

## Non-goals
- Does not implement the GitHub Releases API acquisition itself (that's an acquisition-layer concern, likely folded into GIT-* or a later HTTP source ticket) — this ticket only tags/labels content already normalized by NORM-002/NORM-003 when it's identified as a release-notes source.
- Does not implement semantic diffing of release notes (deferred per design spec §118, "Future Semantic Diffing").

## Simplicity constraints
- This is a thin wrapper/decorator around NORM-002 (Markdown) and NORM-003 (plain text) results, not a new parser. Delegate parsing entirely; only add tagging + section-preservation logic.

## Design
- Package: `internal/normalize/releasenotes`
- Not a new `Normalizer` implementation from scratch — instead, a `Supports`-driven wrapper:
  ```go
  type ReleaseNoteNormalizer struct {
      markdown *markdown.MarkdownNormalizer
      plain    *plaintext.PlainTextNormalizer
  }
  ```
  `Name() == "release-note-normalizer"`, `Version() == "v1"`.
- `Supports(src)`: true when `src.LogicalPath` matches known release-note filenames (`CHANGELOG.md`, `CHANGES.md`, `RELEASES.md`, `HISTORY.md`) case-insensitively, or `src.Metadata["source_type"] == "github-releases"`.
- `Normalize`: delegate to the appropriate underlying normalizer based on file extension, then set `Metadata["content_type"] = "release_note"` on every returned object. For Markdown release notes, additionally use the already-extracted heading hierarchy (from NORM-002) to tag each section object with `Metadata["release_version"]` when a heading matches a version-like pattern (e.g. `^v?\d+\.\d+\.\d+`).

## Inputs / Outputs
- Input: `SourceSnapshot` for a recognized release-notes file.
- Output: `[]domain.KnowledgeObject` from the delegate normalizer, decorated with `content_type=release_note` and (when detectable) `release_version` metadata.

## Failure behavior
- Delegates all parse failures to the underlying normalizer; no additional failure modes introduced.

## Tests
- `CHANGELOG.md` fixture with version-numbered headings: each section tagged with the correct `release_version`.
- Plain-text release notes file: tagged `content_type=release_note` without version sections (acceptable — best-effort).

## Acceptance criteria
- [x] Known release-note filenames are recognized and tagged `content_type=release_note`.
- [x] Version-numbered Markdown headings are captured as `release_version` metadata where the pattern matches.
