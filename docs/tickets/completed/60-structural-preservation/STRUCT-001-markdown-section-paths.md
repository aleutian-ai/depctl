# STRUCT-001: Markdown chunk section paths

**Epic:** Structural Preservation
**Status:** done — see Post-implementation note
**Depends on:** CHUNK-002 (Markdown structural chunker, `docs/tickets/completed/10-fingerprinting-chunking`)
**Estimated size:** small

## Goal
Every Markdown chunk (`internal/data/chunk/markdown`) already carries a display breadcrumb string in `Metadata["heading_path"]` (e.g. `"Transactions > Read transactions > Bucket lookup"`). Add the same ancestor path in structured, array form — `Metadata["section_path"]` as a JSON array (`["Transactions","Read transactions","Bucket lookup"]`) — so downstream consumers (retrieval ranking, MCP breadcrumb formatting, evals) can work with the segments directly instead of re-splitting a `" > "`-joined string.

## Non-goals
- No change to how sections are derived. `splitSections` (`internal/data/chunk/markdown/sections.go`) already walks lines fence-aware and maintains a `headingStack []string`, exactly the ancestor path this ticket needs — this ticket captures that slice, it doesn't recompute structure a different way.
- No rename or removal of the existing `Metadata["heading_path"]` key. It's already relied on by `markdown_test.go` and documented in `docs/tickets/completed/10-fingerprinting-chunking/CHUNK-002-markdown-structural-chunker.md` and `docs/internal/data-chunk.md` — this ticket is purely additive.
- Not touching `domain.KnowledgeObject.Heading` (an existing but currently unpopulated `[]string` field on `KnowledgeObject`) or `Metadata["headings"]` (NORM-002's whole-document breadcrumb list, set by `internal/normalize/markdown/markdown.go`). Those are object-level, pre-chunking; this ticket is chunk-level only, matching CHUNK-002's own scope.
- No AST/goldmark dependency added to the chunker — same fence-aware ATX line scan as today.
- No `section_path` for non-Markdown chunkers (the symbol chunker, `internal/data/chunk/symbol`, has no heading concept).

## Simplicity constraints
- `domain.Chunk.Metadata` stays `map[string]string` — no schema change to `domain.Chunk`. The structured path is a JSON-encoded string under one new key, the same encoding convention already used elsewhere in this codebase for map values that need a compound shape (e.g. `encoding/json` is already an import this package's siblings use).
- One extra field on the existing `section` struct, computed for free where `headingStack` is already maintained — no second pass over the document.
- A part produced by `packSection`'s paragraph-splitting fallback (a section too large for one chunk) inherits the *same* `section_path` as every other part of that section — the path identifies which section a chunk belongs to, not which byte-range within it.

## Design
Package: `internal/data/chunk/markdown` (`sections.go`, `markdown.go`).

`sections.go`:
```go
// section is one heading-delimited slice of a document...
type section struct {
	headingPath string   // existing: " > "-joined display breadcrumb
	sectionPath []string // NEW: the same ancestors, unjoined
	headingLine string
	body        string
}
```
`splitSections`'s `flush` closure already runs at exactly the point `headingStack` holds the section's ancestors; capture a defensive copy there:
```go
flush := func() {
	body := strings.TrimSpace(current.String())
	if body != "" || currentHeadingLine != "" {
		sections = append(sections, section{
			headingPath: currentPath,
			sectionPath: append([]string{}, headingStack...),
			headingLine: currentHeadingLine,
			body:        body,
		})
	}
	current.Reset()
}
```
(`headingStack` is already copied defensively on every heading match via `append(append([]string{}, headingStack[:level-1]...), m[2])`, so this mirrors an existing pattern rather than introducing a new one.)

`markdown.go`'s `Chunk`:
```go
func (c *Chunker) Chunk(ctx context.Context, obj domain.KnowledgeObject) ([]domain.Chunk, error) {
	sections := splitSections(string(obj.Content))

	var chunks []domain.Chunk
	ordinal := 0
	for _, sec := range sections {
		sectionPathJSON, err := json.Marshal(sec.sectionPath)
		if err != nil {
			return nil, fmt.Errorf("markdown chunk: marshal section_path: %w", err)
		}
		for _, part := range packSection(sec.headingLine, sec.body, c.maxChunkBytes) {
			content := []byte(part)
			chunks = append(chunks, domain.Chunk{
				ID:          dchunk.ChunkID(obj.ID, ordinal, content),
				ObjectID:    obj.ID,
				Ordinal:     ordinal,
				Content:     content,
				ContentHash: dchunk.ContentHash(content),
				Metadata: map[string]string{
					"heading_path": sec.headingPath,
					"section_path": string(sectionPathJSON),
				},
			})
			ordinal++
		}
	}
	return chunks, nil
}
```
A section with no headings at all (content before any heading, or a headless document) gets `sec.sectionPath == nil`, which marshals to `"null"` — treated as "no section" by every consumer, same as today's empty `""` `heading_path`.

## Inputs / Outputs
- Input: unchanged — a Markdown/plain-text `domain.KnowledgeObject`.
- Output: unchanged chunk count/boundaries; each `domain.Chunk.Metadata` gains one new key, `"section_path"`, holding a JSON array string. `"heading_path"` is unchanged.

## Failure behavior
- `json.Marshal` on a `[]string` cannot practically fail (no cycles, no unsupported types) — the error return exists for defensive Go style, not because a real failure mode is expected; a chunker returning an error here already aborts the whole object's chunking, same as any other `Chunk` error today.

## Tests
- A single-level-heading document: `section_path` is `["Title"]`, matching the existing `heading_path == "Title"` case in `markdown_test.go`.
- A nested heading (H1 > H2 > H3): `section_path` is the three-element array, `heading_path` is the same three joined by `" > "` — both derived from one `headingStack` snapshot.
- A section split across multiple parts by `packSection` (oversized section): every part carries the identical `section_path`.
- Headless content (no headings at all): `section_path` marshals to `"null"`, `heading_path` is `""` — matches the existing headless-content test case.
- A level-skip heading (H1 straight to H3): `section_path` reflects the same one-deeper-than-stack clamping `headingStack` already applies for `heading_path`.

## Acceptance criteria
- [x] Every Markdown chunk's `Metadata["section_path"]` is a valid JSON array of the chunk's section's ancestor heading text, in order.
- [x] `Metadata["heading_path"]` is unchanged in value and behavior.
- [x] No change to chunk count, ordinals, IDs, or content for any existing fixture.
- [x] `go test ./internal/data/chunk/...` passes, including new `section_path` assertions alongside the existing `heading_path` ones.

## Post-implementation note

Built as designed, with one real bug found and fixed along the way: the ticket's own sketched code, `sectionPath: append([]string{}, headingStack...)`, does *not* produce the `nil` the ticket's own text specifies for headless content ("gets `sec.sectionPath == nil`, which marshals to `"null"`") — `append` onto a non-nil empty slice literal always returns a non-nil (if empty) slice, so headless content actually marshaled to `"[]"`, not `"null"`, contradicting the ticket's own stated `heading_path`-matching convention. Caught by writing the headless-content test the ticket itself specifies, not by inspection — fixed by only copying `headingStack` when non-empty, leaving `sectionPath` genuinely `nil` otherwise.

Also added `TestSectionPathReflectsLevelSkipClamping` (H1 straight to H3), the one test case the ticket's own Tests section named that no existing test happened to cover already.

Full suite green, `-race` clean.
