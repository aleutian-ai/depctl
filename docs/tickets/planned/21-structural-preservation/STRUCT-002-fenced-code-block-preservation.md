# STRUCT-002: Fenced code-block preservation

**Epic:** Structural Preservation
**Status:** planned
**Depends on:** NORM-002 (Markdown normalizer, `docs/tickets/completed/09-normalization`)
**Estimated size:** medium

## Goal
Evolve the Markdown normalizer's fenced-code handling from "languages observed anywhere in the document" (today's `Metadata["code_languages"]`, a deduped, sorted, document-wide set with no content and no position) to structured per-block records: index, language, raw content, and the section path the block sits under. This is `internal/normalize/markdown/normalize.go`'s single existing `ast.Walk` gaining one more thing it collects — not a new parse pass.

## Non-goals
- Not changing what gets chunked or where. `internal/data/chunk/markdown/sections.go`'s `splitSections` already keeps a fence's lines inside its owning section's `body` verbatim (fence lines are written straight into the section's `current` builder, never treated as headings, never dropped) — a code block already stays physically adjacent to its explanatory prose in the chunk that contains it. This ticket adds a *queryable metadata record* of each block; it does not change chunk content or boundaries.
- Not extracting code into a separate `KnowledgeObject` or a separate corpus — the design doc (§6.2) explicitly warns against this ("do not automatically strip code into an isolated corpus with no parent context"); code blocks stay embedded in their normal Markdown object.
- Not removing `Metadata["code_languages"]` — cheap, already relied on nowhere critical enough to break, and a strict subset of the new data (the new records' distinct `language` values), kept for compatibility.
- Not deriving a per-block language from adjacent prose or filename heuristics — only the fence's own info-string language (`FencedCodeBlock.Language`, same source `code_languages` already uses).

## Simplicity constraints
- One new field on `extraction` (`codeBlocks []codeBlockRecord`), populated in the same `case ast.KindFencedCodeBlock:` branch `extract` already has — no second AST walk.
- The record's `section_path` reuses the identical `headingStack` `extract` already maintains for `result.headings` — same snapshot-on-visit technique STRUCT-001 uses in the chunker's own (separate) heading-stack walk. `code_languages`'s existing document-wide dedupe/sort stays as its own thing; the new records are ordered records, not a set.
- `codeBlockRecord` is JSON-marshaled once into a single new `Metadata["code_blocks"]` key on the `KnowledgeObject` — `domain.KnowledgeObject.Metadata` is `map[string]string`, same constraint and same JSON-encoding approach as STRUCT-001's `section_path`.

## Design
Package: `internal/normalize/markdown` (`markdown.go`).

```go
// codeBlockRecord is one fenced code block's structured identity,
// captured at normalization time so a code example is never separated
// from the section that introduces it.
type codeBlockRecord struct {
	Index       int      `json:"index"`        // 0-based, document order
	Language    string   `json:"language"`     // "" if the fence has no info string
	Content     string   `json:"content"`      // raw fenced content, verbatim
	SectionPath []string `json:"section_path"` // ancestor headings at this block's position
}

type extraction struct {
	title      string
	headings   []string
	languages  []string
	links      []string
	codeBlocks []codeBlockRecord // NEW
}
```

`extract`'s existing `ast.KindFencedCodeBlock` case gains the record capture, using the same `headingStack` already tracked for headings:
```go
case ast.KindFencedCodeBlock:
	fcb := node.(*ast.FencedCodeBlock)
	lang := ""
	if l := fcb.Language(source); len(l) > 0 {
		languages[string(l)] = struct{}{}
		lang = string(l)
	}
	result.codeBlocks = append(result.codeBlocks, codeBlockRecord{
		Index:       len(result.codeBlocks),
		Language:    lang,
		Content:     string(fcb.Lines().Value(source)),
		SectionPath: append([]string{}, headingStack...),
	})
```
(`fcb.Lines().Value(source)` is goldmark's documented way to read a fenced block's raw text — `FencedCodeBlock.Text` is deprecated in favor of it, per `ast/block.go`'s own doc comment in the `goldmark v1.8.5` version this repo pins.)

`Normalize` gains one more `metadata[...]` assignment alongside the existing three:
```go
if len(extracted.codeBlocks) > 0 {
	blocksJSON, err := json.Marshal(extracted.codeBlocks)
	if err != nil {
		return nil, fmt.Errorf("markdown: marshal code_blocks for %s: %w", src.LocalPath, err)
	}
	metadata["code_blocks"] = string(blocksJSON)
}
```

## Inputs / Outputs
- Input: unchanged — a `domain.SourceSnapshot` for a `.md`/`.mdx` file.
- Output: unchanged `[]domain.KnowledgeObject` (still exactly one object per NORM-002's existing behavior); the object's `Metadata` gains `"code_blocks"` (JSON array of `codeBlockRecord`, omitted entirely when the document has no fenced code, matching the existing omit-if-empty pattern for `code_languages`/`links`). `Metadata["code_languages"]` is unchanged.

## Failure behavior
- `json.Marshal` failure on `[]codeBlockRecord` is not a realistic runtime case (no cycles, only strings/ints/slices) but is checked and wrapped, matching this normalizer's existing error style (`fmt.Errorf("markdown: ...: %w", err)`) rather than silently dropping the metadata.
- A fence with an empty info string (no language) still produces a record with `Language: ""` — a code block is not skipped just because it isn't language-tagged; the whole point is preserving it, tagged or not.

## Tests
- A document with one fenced code block in a named section: `code_blocks` has one record with the right `index` (0), `language`, `content` (byte-for-byte the fence's interior), and `section_path` matching that section's heading ancestors.
- A document with multiple fenced blocks across different sections and nesting levels: `index` increments in document order, each record's `section_path` matches its own position, not a shared/last-seen one.
- A fence with no info string: record has `Language: ""`, still present (not dropped), and `code_languages`'s existing set is unaffected (empty-language fences were already excluded from that set before this ticket).
- A document with no fenced code at all: `Metadata["code_blocks"]` key is absent, matching the existing omit-when-empty behavior of `code_languages`/`links`.
- A fence inside a section that itself gets split across multiple chunks later (CHUNK-002): out of scope to assert chunk-level linkage here (that's STRUCT-003/CHUNK-002's job) — this ticket's tests stay at the normalizer/`KnowledgeObject` level only.

## Acceptance criteria
- [ ] Every fenced code block in a normalized Markdown document produces one `codeBlockRecord` in `Metadata["code_blocks"]`, in document order.
- [ ] Each record's `section_path` matches the block's actual position in the heading hierarchy at normalization time.
- [ ] `Metadata["code_languages"]` behavior is unchanged (still the deduped, sorted, document-wide set).
- [ ] No fenced code content is extracted into a separate object, source, or corpus — it stays embedded in the single Markdown `KnowledgeObject`'s `Content` exactly as before.
- [ ] `go test ./internal/normalize/markdown/...` passes, including new `code_blocks` assertions.
