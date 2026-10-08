// Package symbol implements depctl's symbol chunker (CHUNK-003):
// NORM-004 already emits one KnowledgeObject per exported Go symbol, so
// this chunker's job is close to a 1:1 passthrough with metadata
// shaping — one chunk per symbol_doc/package_doc object, no splitting or
// merging.
package symbol

import (
	"bytes"
	"context"

	dchunk "github.com/aleutian-ai/depctl/internal/data/chunk"
	"github.com/aleutian-ai/depctl/internal/domain"
)

// Chunker implements chunk.Chunker for NORM-004-produced KnowledgeObjects
// (ContentType "symbol_doc" or "package_doc").
type Chunker struct{}

// New returns a ready-to-use symbol chunker.
func New() *Chunker {
	return &Chunker{}
}

// Chunk emits exactly one domain.Chunk wrapping obj's full content, with
// package/symbol/signature/source_path/version/dependency/source_type
// metadata promoted from obj. Missing metadata fields (e.g. no signature
// for a constant) are left as empty strings rather than causing an error.
func (c *Chunker) Chunk(ctx context.Context, obj domain.KnowledgeObject) ([]domain.Chunk, error) {
	metadata := map[string]string{
		"package":     obj.Metadata["package"],
		"symbol":      obj.Metadata["symbol"],
		"signature":   obj.Metadata["signature"],
		"source_path": obj.LogicalPath,
		"version":     obj.Version,
		// STRUCT-003: promoted from the parent KnowledgeObject so a
		// caller holding only this Chunk never needs a second lookup to
		// know its dependency/source-type identity.
		"dependency":  obj.Dependency.Dependency.Name,
		"source_type": obj.SourceType,
	}

	content := obj.Content
	if len(bytes.TrimSpace(content)) == 0 {
		// NORM-004's Content is deliberately just the doc comment,
		// which is empty for any exported symbol without one — measured
		// at ~11% of real-world exported symbols (hack/chunk-sweep
		// against ~21k real objects). Rather than ship a chunk with
		// genuinely empty Content (zero signal for embedding/retrieval
		// later), fall back to the signature already sitting in
		// metadata — still real, useful information about the symbol.
		content = []byte(fallbackContent(metadata))
	}

	return []domain.Chunk{{
		ID:          dchunk.ChunkID(obj.ID, 0, content),
		ObjectID:    obj.ID,
		Ordinal:     0,
		Content:     content,
		ContentHash: dchunk.ContentHash(content),
		Metadata:    metadata,
	}}, nil
}

// fallbackContent builds a minimal, non-empty Content string from
// metadata alone, for a symbol with no doc comment: prefer the
// signature (the single most informative field), then package.symbol,
// then whichever single identifying field is available.
func fallbackContent(metadata map[string]string) string {
	if sig := metadata["signature"]; sig != "" {
		return sig
	}
	pkg, symbol := metadata["package"], metadata["symbol"]
	switch {
	case pkg != "" && symbol != "":
		return pkg + "." + symbol
	case symbol != "":
		return symbol
	default:
		return pkg
	}
}
