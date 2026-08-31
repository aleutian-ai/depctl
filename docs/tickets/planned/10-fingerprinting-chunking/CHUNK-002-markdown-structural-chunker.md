# CHUNK-002: Markdown structural chunker

**Epic:** Fingerprinting and Chunking
**Status:** planned
**Depends on:** CHUNK-001
**Estimated size:** medium

## Goal
Chunk Markdown-derived `KnowledgeObject`s primarily by heading section, splitting further at paragraph boundaries only when a section exceeds a configurable size bound, preserving the heading path in each chunk's metadata.

## Non-goals
- Does not re-parse Markdown from scratch — operates on the heading-hierarchy metadata already produced by NORM-002.
- Does not implement token-accurate counting via a real tokenizer — a byte-length or simple whitespace-word-count upper bound is sufficient for v0.1 (avoids pulling in a tokenizer dependency prematurely).

## Simplicity constraints
- Use a single configurable upper bound (bytes or words — pick one, document it) rather than building a pluggable size-metric abstraction.
- Paragraph-boundary splitting is a simple "split on blank-line-separated blocks and greedily pack under the limit" — not a smart semantic splitter.

## Design
- Package: `internal/data/chunk/markdown`
- `MarkdownChunker` implements `Chunker` (CHUNK-001).
- Config: `MaxChunkBytes int` (default e.g. 2000 bytes), passed via constructor, sourced from global config (CLI-001) with a hardcoded fallback default.
- Algorithm:
  1. Group the object's content by heading section (using the heading-hierarchy metadata from NORM-002).
  2. If a section's byte length ≤ `MaxChunkBytes`, emit one chunk for the whole section.
  3. Else, split at paragraph (blank-line) boundaries, greedily packing consecutive paragraphs into chunks up to the limit.
  4. Every chunk carries `Metadata["heading_path"]` (e.g. `"Getting Started > Installation"`) so retrieval results can show context even when a chunk is a sub-part of a section.

## Inputs / Outputs
- Input: a Markdown-derived `domain.KnowledgeObject` (with heading metadata from NORM-002).
- Output: `[]domain.Chunk`, each with `ObjectID`, `ID` (via CHUNK-001's `ChunkID`), `Content`, `Metadata["heading_path"]`, ordinal.

## Failure behavior
- Object with no heading metadata (e.g. plain-text-normalized content misrouted here): treat the whole content as one section and proceed — do not error.

## Tests
- Small section under the limit → one chunk.
- Large section over the limit → split at paragraph boundaries, each resulting chunk ≤ `MaxChunkBytes` (allow slight overage only if a single paragraph itself exceeds the limit — document that edge case as accepted).
- Heading path correctly threaded through multi-level nested headings.
- Chunk IDs stable across repeated runs on identical input.

## Acceptance criteria
- [ ] Sections under the size limit produce exactly one chunk.
- [ ] Oversized sections split at paragraph boundaries with heading path preserved.
- [ ] Chunk IDs are deterministic across repeated chunking of identical content.
