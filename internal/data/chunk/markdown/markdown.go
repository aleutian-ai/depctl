// Package markdown implements depctl's Markdown structural chunker
// (CHUNK-002): splits a Markdown-derived KnowledgeObject primarily by
// heading section, falling back to paragraph-boundary packing only when
// a section exceeds a configurable size bound. It re-derives section
// boundaries directly from the object's Content by scanning for ATX
// (`#`) heading lines — not a full Markdown re-parse (no AST, no
// goldmark dependency here), just enough structure-awareness (including
// staying fence-aware, so a "# comment" inside a ```bash block is never
// mistaken for a heading) to split correctly.
package markdown

import (
	"context"
	"encoding/json"
	"fmt"

	dchunk "github.com/aleutian-ai/depctl/internal/data/chunk"
	"github.com/aleutian-ai/depctl/internal/domain"
)

// DefaultMaxChunkBytes bounds a chunk's size when no explicit limit is
// configured. A byte-length bound, not a real-tokenizer count — good
// enough for v0.1 per the ticket's own simplicity constraint.
const DefaultMaxChunkBytes = 2000

// Chunker implements chunk.Chunker for Markdown/plain-text
// KnowledgeObjects (ContentType "markdown" or "text").
type Chunker struct {
	maxChunkBytes int
}

// New returns a Chunker bounding chunks to maxChunkBytes; a
// non-positive value falls back to DefaultMaxChunkBytes.
func New(maxChunkBytes int) *Chunker {
	if maxChunkBytes <= 0 {
		maxChunkBytes = DefaultMaxChunkBytes
	}
	return &Chunker{maxChunkBytes: maxChunkBytes}
}

// Chunk splits obj.Content into heading-section chunks, further
// splitting any section over maxChunkBytes at paragraph boundaries. An
// object with no headings at all (e.g. plain-text-normalized content
// misrouted here) is treated as a single section, not an error.
func (c *Chunker) Chunk(ctx context.Context, obj domain.KnowledgeObject) ([]domain.Chunk, error) {
	sections := splitSections(string(obj.Content))

	var chunks []domain.Chunk
	ordinal := 0
	for _, sec := range sections {
		// STRUCT-001: the same ancestor path as heading_path, structured
		// (a JSON array) rather than pre-joined, so downstream consumers
		// can work with the segments directly instead of re-splitting a
		// " > "-joined display string. A part produced by packSection's
		// paragraph-splitting fallback inherits the same section_path as
		// every other part of that section — the path identifies which
		// section a chunk belongs to, not which byte-range within it.
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
					// STRUCT-003: promoted from the parent KnowledgeObject,
					// same rationale as the symbol chunker's own addition.
					"dependency":  obj.Dependency.Dependency.Name,
					"version":     obj.Version,
					"source_type": obj.SourceType,
				},
			})
			ordinal++
		}
	}
	return chunks, nil
}
