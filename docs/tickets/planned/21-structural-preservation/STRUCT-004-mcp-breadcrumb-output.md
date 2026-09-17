# STRUCT-004: MCP breadcrumb output

**Epic:** Structural Preservation
**Status:** planned
**Depends on:** STRUCT-001, STRUCT-003
**Estimated size:** small

## Goal
Compute a single human-readable breadcrumb string per search result — `grpc-go@1.72.0 > Authentication > Transport Credentials > NewTLS` for a prose/Markdown chunk, `go.etcd.io/bbolt@1.3.11 > tx.go > (*Tx).Bucket` for a symbol chunk — and surface it on `query.ResultChunk` and the `search_dependency_docs` MCP tool's output, per the design doc's §13.1 example. An agent should see structural location alongside content without parsing anything itself.

## Non-goals
- No new stored field. The breadcrumb is computed once, at query-result-build time, in `internal/query/search.go`, from data STRUCT-003 already put on the fetched `domain.Chunk.Metadata` plus the dependency/version `search` already has from `backend.PointMetadata` — it is never written to Badger or the vector backend.
- No change to ranking, filtering, or which chunks are returned — purely additive output formatting.
- No breadcrumb-parsing or breadcrumb-based search in this ticket (e.g. "find everything under Authentication") — that's a future retrieval feature, not in scope here.
- `internal/mcp/tools.go`'s `searchDependencyDocsHandler` gains only a field copy, matching its existing per-field mapping style — no new logic duplicated there; all breadcrumb construction lives once, in `internal/query`.

## Simplicity constraints
- One small package-private helper in `internal/query`, called from the one place (`search.go`'s `search` function) that already builds every `ResultChunk` — no per-caller duplication.
- The helper switches on which metadata a chunk actually has (`section_path` for Markdown chunks, `symbol`+`source_path` for symbol chunks) rather than requiring a `ContentType`/chunk-kind field that doesn't exist on `domain.Chunk` today — matches what STRUCT-003 actually put there, not a new discriminator.
- A chunk with neither (some future or malformed chunk type) still gets a valid, non-empty breadcrumb: just `dependency@version` — never an empty string, never a template gap like `"@ > > "`.

## Design
Package: `internal/query` (`search.go`), `internal/mcp` (`tools.go`).

```go
// breadcrumb builds a display string identifying a chunk's structural
// position: "dependency@version" plus whatever structural segments the
// chunk's own metadata carries (a Markdown section path, or a symbol
// chunk's file/symbol), per STRUCT-003's promoted fields.
func breadcrumb(dependency, version string, metadata map[string]string) string {
	head := dependency
	if version != "" {
		head += "@" + version
	}

	var segments []string
	if raw := metadata["section_path"]; raw != "" && raw != "null" {
		var path []string
		if err := json.Unmarshal([]byte(raw), &path); err == nil {
			segments = path
		}
	} else if symbol := metadata["symbol"]; symbol != "" {
		if sourcePath := metadata["source_path"]; sourcePath != "" {
			segments = append(segments, sourcePath)
		}
		segments = append(segments, symbol)
	}

	if len(segments) == 0 {
		return head
	}
	return head + " > " + strings.Join(segments, " > ")
}
```

`search.go`'s `search` function, in its existing `for _, p := range result.Points` loop, right after fetching `chunk`:
```go
chunks = append(chunks, ResultChunk{
	ChunkID:    p.ID,
	Content:    string(chunk.Content),
	Score:      p.Score,
	Ecosystem:  p.Metadata.Ecosystem,
	Dependency: p.Metadata.Dependency,
	Version:    p.Metadata.Version,
	Generation: p.Metadata.Generation,
	SourceType: p.Metadata.SourceType,
	Authority:  p.Metadata.Authority,
	TrustClass: generation.TrustClassForSourceType(p.Metadata.SourceType),
	Breadcrumb: breadcrumb(p.Metadata.Dependency, p.Metadata.Version, chunk.Metadata), // NEW
})
```

`query.go`'s `ResultChunk` gains one field:
```go
type ResultChunk struct {
	ChunkID    string
	Content    string
	Score      float32
	Ecosystem  string
	Dependency string
	Version    string
	Generation string
	SourceType string
	Authority  int
	TrustClass domain.TrustClass
	Breadcrumb string // NEW — see breadcrumb() in search.go
}
```

`internal/mcp/tools.go`'s `SearchResultChunk` and `searchDependencyDocsHandler` gain the matching field/copy, same pattern as every other field already there:
```go
type SearchResultChunk struct {
	ChunkID    string            `json:"chunk_id"`
	Content    string            `json:"content"`
	Score      float32           `json:"score"`
	Ecosystem  string            `json:"ecosystem"`
	Dependency string            `json:"dependency"`
	Version    string            `json:"version"`
	Generation string            `json:"generation"`
	SourceType string            `json:"source_type"`
	Authority  int               `json:"authority"`
	TrustClass domain.TrustClass `json:"trust_class" jsonschema:"..."`
	Breadcrumb string            `json:"breadcrumb" jsonschema:"structural location of this chunk within its dependency/version, e.g. \"grpc-go@1.72.0 > Authentication > Transport Credentials > NewTLS\""`
}
```
and in the handler's existing per-field copy:
```go
out.Chunks[i] = SearchResultChunk{
	ChunkID: c.ChunkID, Content: c.Content, Score: c.Score,
	Ecosystem: c.Ecosystem, Dependency: c.Dependency, Version: c.Version,
	Generation: c.Generation, SourceType: c.SourceType, Authority: c.Authority,
	TrustClass: c.TrustClass, Breadcrumb: c.Breadcrumb,
}
```

## Inputs / Outputs
- Input: unchanged — `query.Query`/`SearchDependencyDocsIn`.
- Output: `query.SearchResult.Chunks[i].Breadcrumb` and `SearchDependencyDocsOut.Chunks[i].Breadcrumb` — a non-empty string on every result, from a bare `dependency@version` (no structural metadata available) up to a full section/symbol path.

## Failure behavior
- A malformed `section_path` JSON (should not happen post-STRUCT-001, but defensively) falls back to no structural segments rather than erroring the whole search — `json.Unmarshal`'s error is swallowed intentionally, same principle as `search.go`'s own "a point the backend returned but Badger no longer has is skipped, not fatal" tolerance elsewhere in this function.
- A chunk with no `Metadata` at all (nil map) is handled by Go's zero-value map-read semantics (`metadata["section_path"]` on a nil map returns `""`, no panic) — no explicit nil check needed.

## Tests
- A Markdown chunk with a `section_path` of `["Authentication","Transport Credentials"]` and `dependency="grpc-go"`/`version="1.72.0"` produces `"grpc-go@1.72.0 > Authentication > Transport Credentials"`.
- A symbol chunk with `symbol="(*Tx).Bucket"`, `source_path="tx.go"`, `dependency="go.etcd.io/bbolt"`, `version="1.3.11"` produces `"go.etcd.io/bbolt@1.3.11 > tx.go > (*Tx).Bucket"`.
- A chunk with no section/symbol metadata at all produces just `"dependency@version"`.
- A chunk with empty `version` produces `"dependency"` with no trailing `@` — no `"dependency@"` artifact.
- `SearchDependencyDocsOut`'s JSON round-trips with the new `breadcrumb` field present and correctly populated for both a Markdown- and symbol-sourced result in an end-to-end MCP test.

## Acceptance criteria
- [ ] `query.ResultChunk.Breadcrumb` is populated for every search result, never empty.
- [ ] `search_dependency_docs`'s output includes a `breadcrumb` field per chunk, matching `ResultChunk.Breadcrumb`.
- [ ] A Markdown-sourced chunk's breadcrumb reflects its section path; a symbol-sourced chunk's breadcrumb reflects its file and symbol.
- [ ] No change to which chunks are returned, their order, or their scores.
