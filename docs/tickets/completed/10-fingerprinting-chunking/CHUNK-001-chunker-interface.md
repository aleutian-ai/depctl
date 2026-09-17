# CHUNK-001: Chunker interface

**Epic:** Fingerprinting and Chunking
**Status:** done
**Depends on:** NORM-001
**Estimated size:** small

## Goal
Define the shared `Chunker` interface used by content-type-specific chunkers (Markdown structural, symbol) and the deterministic chunk ID scheme.

## Non-goals
- Does not implement any concrete chunker (CHUNK-002, CHUNK-003).

## Simplicity constraints
- Interface only, mirroring NORM-001's approach — no shared base chunker until a second implementation proves what's actually common.
- Never adopt a single universal "N tokens with M overlap" default chunker — the design spec explicitly forbids this; chunking must stay content-aware per CHUNK-002/003.

## Design
- Package: `internal/data/chunk`
- ```go
  type Chunker interface {
      Chunk(ctx context.Context, obj domain.KnowledgeObject) ([]domain.Chunk, error)
  }
  ```
- Chunk ID: deterministic, derived as `object ID + ordinal` hashed together, e.g.:
  ```go
  func ChunkID(objectID string, ordinal int, content []byte) string
  ```
  implemented similarly to HASH-002 (BLAKE3 over `objectID + varint(ordinal) + content`, base32-encoded, prefixed `chk_`). Using content in the hash (not just ordinal) means a chunk whose boundaries shift due to upstream edits gets a new ID rather than silently reusing stale content under an old ID.
- A `Registry` type analogous to NORM-001's, selecting a chunker by the object's `ContentType`/`SourceType` metadata (e.g. `package_doc`/`symbol_doc` → symbol chunker, `text`/markdown sections → markdown chunker).

## Inputs / Outputs
- Input: a single `domain.KnowledgeObject`.
- Output: `[]domain.Chunk`.

## Failure behavior
- Returns a typed error; caller aggregates per-object failures similarly to normalization.

## Tests
- Fake chunker satisfies the interface and produces deterministic IDs for identical input.
- `Registry.Select` dispatches based on content type correctly (with two fake chunkers registered).

## Acceptance criteria
- [x] Interface compiles, used by a fake implementation in tests.
- [x] Chunk ID scheme is deterministic and documented.
