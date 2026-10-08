// Package chunk defines the shared Chunker interface used by every
// content-type-specific chunker (Markdown structural, symbol), plus the
// deterministic chunk ID and content-hash schemes they all use.
// Chunking must stay content-aware per chunker — this package never
// grows a universal "N tokens with M overlap" default.
package chunk

import (
	"context"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"strings"

	"github.com/zeebo/blake3"

	"github.com/aleutian-ai/depctl/internal/domain"
)

// chunkIDEncoding is lowercase, unpadded base32 — matches
// internal/data/fingerprint's ObjectID convention (human-inspectable,
// storage-key-safe), not internal/resolver.Fingerprint's uppercase one.
var chunkIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Chunker splits a normalized KnowledgeObject into retrieval-sized
// Chunks using a content-aware strategy.
type Chunker interface {
	Chunk(ctx context.Context, obj domain.KnowledgeObject) ([]domain.Chunk, error)
}

// Registry selects a Chunker by a KnowledgeObject's ContentType, trying
// registered entries in registration order. Chunker has no Supports
// method (unlike normalize.Normalizer) — dispatch here is a
// straightforward content-type lookup, not arbitrary per-chunker logic,
// so the registry owns the matching rule directly rather than delegating
// it to each chunker.
type Registry struct {
	entries []registryEntry
}

type registryEntry struct {
	contentTypes map[string]bool
	chunker      Chunker
}

// NewRegistry returns an empty Registry; register chunkers via Register.
func NewRegistry() *Registry {
	return &Registry{}
}

// Register associates chunker with one or more ContentType values and
// returns the Registry, so registrations can be chained.
func (r *Registry) Register(chunker Chunker, contentTypes ...string) *Registry {
	set := make(map[string]bool, len(contentTypes))
	for _, ct := range contentTypes {
		set[ct] = true
	}
	r.entries = append(r.entries, registryEntry{contentTypes: set, chunker: chunker})
	return r
}

// Select returns the first registered Chunker whose content types
// include obj.ContentType, or (nil, false) if none match.
func (r *Registry) Select(obj domain.KnowledgeObject) (Chunker, bool) {
	for _, e := range r.entries {
		if e.contentTypes[obj.ContentType] {
			return e.chunker, true
		}
	}
	return nil, false
}

// ChunkID derives a deterministic "chk_"-prefixed ID from the parent
// object's ID, this chunk's ordinal, and its content. Including content
// (not just ordinal) means a chunk whose boundaries shift due to an
// upstream edit gets a new ID rather than silently reusing stale content
// under an old one.
func ChunkID(objectID string, ordinal int, content []byte) string {
	h := blake3.New()
	writeLengthPrefixed(h, []byte(objectID))
	var ordBuf [8]byte
	binary.BigEndian.PutUint64(ordBuf[:], uint64(ordinal))
	h.Write(ordBuf[:])
	writeLengthPrefixed(h, content)

	sum := h.Sum(nil)
	return "chk_" + strings.ToLower(chunkIDEncoding.EncodeToString(sum))
}

// ContentHash returns a hex-encoded BLAKE3 digest of content alone —
// independent of ChunkID, which also incorporates the parent object ID
// and ordinal. Useful for detecting whether a chunk's content changed
// independent of its position.
func ContentHash(content []byte) string {
	sum := blake3.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// writeLengthPrefixed writes a uvarint length followed by b, so
// concatenating variable-length fields can never produce an ambiguous
// hash input.
func writeLengthPrefixed(h *blake3.Hasher, b []byte) {
	var lenBuf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lenBuf[:], uint64(len(b)))
	h.Write(lenBuf[:n])
	h.Write(b)
}
