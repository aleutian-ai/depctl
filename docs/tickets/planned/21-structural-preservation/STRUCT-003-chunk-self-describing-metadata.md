# STRUCT-003: Chunk self-describing metadata

**Epic:** Structural Preservation
**Status:** planned
**Depends on:** STRUCT-001, CHUNK-002/003 (`docs/tickets/completed/10-fingerprinting-chunking`)
**Estimated size:** medium

## Goal
Promote the provenance fields a chunk's parent `KnowledgeObject` already carries — dependency, version, source type, package/file/symbol identity, section path — directly onto that chunk's own `Metadata`, at chunk-build time, when the chunker already has the full `domain.KnowledgeObject` in hand. Today `internal/query/search.go`'s `search` fetches a `domain.Chunk` via one `s.data.GetChunk(ctx, p.Metadata.Generation, p.ID)` call per result and only reads `chunk.Content` — its `Metadata` map is already in memory at that point but nothing looks at it. This ticket makes that one already-happening call sufficient to build a fully self-describing query result, instead of requiring a second Badger lookup of the parent object if a consumer ever needed package/file/symbol/section identity (which STRUCT-004 does).

## Non-goals
- No new Badger read path, no new store method — this ticket only changes what the two existing chunkers (`internal/data/chunk/markdown`, `internal/data/chunk/symbol`) write into `domain.Chunk.Metadata`, using fields already present on the `domain.KnowledgeObject` they're already given.
- Not touching `backend.PointMetadata` or the vector backend's payload schema (`internal/backend`, `internal/backend/qdrant`) — `Ecosystem`/`Dependency`/`Version`/`Generation`/`SourceType`/`Authority` are already carried there and already flow into `query.ResultChunk` via `p.Metadata` in `search.go`; duplicating them onto `Chunk.Metadata` too would be redundant for those specific fields. This ticket adds the fields `PointMetadata` has no room for: package, file/source_path, symbol, section_path.
- Not changing `query.ResultChunk` or MCP output — that's STRUCT-004, which depends on this ticket's chunk-level data existing first.
- Not adding these fields for chunk types where they don't apply (e.g. no `symbol`/`section_path` conflict — the symbol chunker never sets `section_path`, the Markdown chunker never sets `symbol`).

## Simplicity constraints
- Every field this ticket adds is already sitting on the `obj domain.KnowledgeObject` each `Chunker.Chunk` method receives (`obj.Dependency`, `obj.SourceType`, `obj.LogicalPath`, `obj.Metadata["package"]`/`["symbol"]`, and — for Markdown — STRUCT-001's per-section `sectionPath`). No new lookups, no new interfaces.
- The symbol chunker (`internal/data/chunk/symbol/symbol.go`) already promotes `package`/`symbol`/`signature`/`source_path`/`version` onto its one emitted chunk's `Metadata` — this ticket only adds the two fields it's still missing (`dependency`, `source_type`), matching the existing key names it already uses rather than inventing new ones.
- The Markdown chunker (`internal/data/chunk/markdown/markdown.go`) currently sets only `heading_path` (+ STRUCT-001's `section_path`) — this ticket adds `dependency`, `version`, `source_type` there too, using the same key names the symbol chunker already established, so a consumer reading `Metadata["dependency"]` doesn't need to know which chunker produced a given chunk.

## Design
Package: `internal/data/chunk/symbol` (`symbol.go`) and `internal/data/chunk/markdown` (`markdown.go`). No interface or struct changes — same `Chunker.Chunk(ctx, obj) ([]domain.Chunk, error)` signature (`internal/data/chunk/chunk.go`) both already implement.

`symbol.go`'s existing metadata map gains two keys:
```go
metadata := map[string]string{
	"package":     obj.Metadata["package"],
	"symbol":      obj.Metadata["symbol"],
	"signature":   obj.Metadata["signature"],
	"source_path": obj.LogicalPath,
	"version":     obj.Version,
	"dependency":  obj.Dependency.Dependency.Name,   // NEW
	"source_type": obj.SourceType,                    // NEW
}
```

`markdown.go`'s `Chunk` gains the same two keys plus `version`, alongside STRUCT-001's `heading_path`/`section_path`:
```go
chunks = append(chunks, domain.Chunk{
	ID:          dchunk.ChunkID(obj.ID, ordinal, content),
	ObjectID:    obj.ID,
	Ordinal:     ordinal,
	Content:     content,
	ContentHash: dchunk.ContentHash(content),
	Metadata: map[string]string{
		"heading_path": sec.headingPath,
		"section_path": string(sectionPathJSON),
		"dependency":   obj.Dependency.Dependency.Name, // NEW
		"version":      obj.Version,                     // NEW
		"source_type":  obj.SourceType,                  // NEW
	},
})
```
`obj.Dependency.Dependency.Name` matches the field path `symbol.go` already uses (`domain.KnowledgeObject.Dependency` is a `domain.DependencyVersion`, whose `Dependency` field is a `domain.Dependency` with `.Name`) — no new field derivation, just applying the same access pattern in both chunkers.

## Inputs / Outputs
- Input: unchanged — the same `domain.KnowledgeObject` each chunker already receives.
- Output: unchanged chunk count/boundaries/IDs/content; each chunk's `Metadata` gains `dependency`/`source_type` (symbol chunker) or `dependency`/`version`/`source_type` (Markdown chunker) — all populated from fields already on `obj`, never empty unless `obj`'s own field was empty.

## Failure behavior
- None of this ticket's changes can fail independently of the chunker's existing error paths — it's pure field assignment from already-validated `obj` data (a `KnowledgeObject` reaching a chunker has already passed `KnowledgeObject.Validate()` upstream).
- An `obj.Dependency.Dependency.Name` or `obj.SourceType` that's legitimately empty (e.g. a malformed or partially-populated fixture in a test) produces an empty string in the chunk's metadata, not an error — matches the symbol chunker's existing "missing metadata fields are left as empty strings" behavior for `signature`.

## Tests
- Symbol chunker: a fixture `KnowledgeObject` with `Dependency`/`SourceType` set produces a chunk whose `Metadata["dependency"]`/`Metadata["source_type"]` match, alongside the existing `package`/`symbol`/`signature`/`source_path`/`version` assertions already in `symbol_test.go`.
- Markdown chunker: a fixture object produces every chunk with `dependency`/`version`/`source_type` matching `obj`'s fields, alongside STRUCT-001's `section_path` and the existing `heading_path`.
- A chunk whose parent object has an empty `SourceType`/`Dependency` (edge-case fixture): the corresponding metadata key is present with an empty string value, not omitted and not an error.
- `go test ./internal/data/chunk/...` passes with the new metadata keys asserted for both chunkers.

## Acceptance criteria
- [ ] Every Markdown chunk's `Metadata` includes `dependency`, `version`, `source_type`, in addition to the existing `heading_path` and STRUCT-001's `section_path`.
- [ ] Every symbol chunk's `Metadata` includes `dependency`, `source_type`, in addition to the existing `package`, `symbol`, `signature`, `source_path`, `version`.
- [ ] No change to chunk count, ordinals, IDs, or content for any existing fixture.
- [ ] A caller holding only a `domain.Chunk` (as returned by `data/badger`'s `GetChunk`) can determine its dependency, version, source type, and structural position without any further store lookup.
