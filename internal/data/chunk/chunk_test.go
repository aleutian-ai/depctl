package chunk

import (
	"context"
	"regexp"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

// fakeChunker is a minimal Chunker used to exercise the interface
// contract and Registry.Select without any real content-splitting logic.
type fakeChunker struct {
	name string
}

func (f *fakeChunker) Chunk(ctx context.Context, obj domain.KnowledgeObject) ([]domain.Chunk, error) {
	id := ChunkID(obj.ID, 0, obj.Content)
	return []domain.Chunk{{ID: id, ObjectID: obj.ID, Content: obj.Content}}, nil
}

func TestFakeChunkerProducesDeterministicIDs(t *testing.T) {
	f := &fakeChunker{name: "fake"}
	obj := domain.KnowledgeObject{ID: "ko_abc", Content: []byte("hello")}

	a, err := f.Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	b, err := f.Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if a[0].ID != b[0].ID {
		t.Errorf("ChunkID not deterministic: %s != %s", a[0].ID, b[0].ID)
	}
}

func TestRegistrySelectDispatchesByContentType(t *testing.T) {
	md := &fakeChunker{name: "markdown"}
	sym := &fakeChunker{name: "symbol"}
	reg := NewRegistry().
		Register(sym, "symbol_doc", "package_doc").
		Register(md, "markdown", "text")

	got, ok := reg.Select(domain.KnowledgeObject{ContentType: "symbol_doc"})
	if !ok || got != Chunker(sym) {
		t.Errorf("Select(symbol_doc) = %v, %v, want the symbol chunker", got, ok)
	}

	got, ok = reg.Select(domain.KnowledgeObject{ContentType: "markdown"})
	if !ok || got != Chunker(md) {
		t.Errorf("Select(markdown) = %v, %v, want the markdown chunker", got, ok)
	}

	_, ok = reg.Select(domain.KnowledgeObject{ContentType: "unknown"})
	if ok {
		t.Error("Select(unknown) = true, want false (no registered chunker)")
	}
}

func TestChunkIDDeterministicAndFormatted(t *testing.T) {
	a := ChunkID("ko_abc", 0, []byte("hello"))
	b := ChunkID("ko_abc", 0, []byte("hello"))
	if a != b {
		t.Errorf("ChunkID not deterministic: %s != %s", a, b)
	}
	if !regexp.MustCompile(`^chk_[a-z2-7]+$`).MatchString(a) {
		t.Errorf("ChunkID %q does not match ^chk_[a-z2-7]+$", a)
	}
}

func TestChunkIDDiffersWhenContentShifts(t *testing.T) {
	// The whole point of hashing content (not just ordinal) into the ID:
	// a chunk whose boundaries moved due to an upstream edit must get a
	// new ID, not silently reuse a stale one.
	a := ChunkID("ko_abc", 0, []byte("original content"))
	b := ChunkID("ko_abc", 0, []byte("edited content"))
	if a == b {
		t.Error("ChunkID should differ when content differs at the same ordinal")
	}
}

func TestChunkIDDiffersByOrdinal(t *testing.T) {
	a := ChunkID("ko_abc", 0, []byte("same content"))
	b := ChunkID("ko_abc", 1, []byte("same content"))
	if a == b {
		t.Error("ChunkID should differ by ordinal, all else equal")
	}
}

func TestContentHashDeterministic(t *testing.T) {
	a := ContentHash([]byte("hello"))
	b := ContentHash([]byte("hello"))
	if a != b {
		t.Errorf("ContentHash not deterministic: %s != %s", a, b)
	}
	if ContentHash([]byte("hello")) == ContentHash([]byte("world")) {
		t.Error("ContentHash collided for different content")
	}
}
