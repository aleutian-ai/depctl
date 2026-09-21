package badger

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	bg "github.com/dgraph-io/badger/v4"

	"aleutian-ai/ragctl/internal/domain"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "badger")
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, dir
}

func TestKnowledgeObjectRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	obj := domain.KnowledgeObject{ID: "ko_1", Title: "Example", Content: []byte("hello")}
	if err := s.PutKnowledgeObject(ctx, obj); err != nil {
		t.Fatalf("PutKnowledgeObject: %v", err)
	}

	got, err := s.GetKnowledgeObject(ctx, "ko_1")
	if err != nil {
		t.Fatalf("GetKnowledgeObject: %v", err)
	}
	if got.Title != obj.Title || string(got.Content) != string(obj.Content) {
		t.Errorf("GetKnowledgeObject = %+v, want %+v", got, obj)
	}

	if err := s.DeleteKnowledgeObject(ctx, "ko_1"); err != nil {
		t.Fatalf("DeleteKnowledgeObject: %v", err)
	}
	if _, err := s.GetKnowledgeObject(ctx, "ko_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetKnowledgeObject after delete: err = %v, want ErrNotFound", err)
	}
}

func TestGetKnowledgeObjectNotFound(t *testing.T) {
	s, _ := openTestStore(t)
	if _, err := s.GetKnowledgeObject(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetKnowledgeObject = %v, want ErrNotFound", err)
	}
}

func TestRestartPersistence(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "badger")
	ctx := context.Background()

	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.PutKnowledgeObject(ctx, domain.KnowledgeObject{ID: "ko_1", Content: []byte("hello")}); err != nil {
		t.Fatalf("PutKnowledgeObject: %v", err)
	}
	if err := s.PutChunk(ctx, "gen_1", domain.Chunk{ID: "chk_1", ObjectID: "ko_1", Content: []byte("hello")}); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	obj, err := s2.GetKnowledgeObject(ctx, "ko_1")
	if err != nil {
		t.Fatalf("GetKnowledgeObject after reopen: %v", err)
	}
	if string(obj.Content) != "hello" {
		t.Errorf("obj.Content after reopen = %q, want %q", obj.Content, "hello")
	}

	c, err := s2.GetChunk(ctx, "gen_1", "chk_1")
	if err != nil {
		t.Fatalf("GetChunk after reopen: %v", err)
	}
	if string(c.Content) != "hello" {
		t.Errorf("chunk.Content after reopen = %q, want %q", c.Content, "hello")
	}
}

func TestBatchWriteAndListGenerationChunks(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	const n = 25
	for i := 0; i < n; i++ {
		c := domain.Chunk{ID: fmt.Sprintf("chk_%d", i), ObjectID: "ko_1", Ordinal: i, Content: []byte("x")}
		if err := s.PutChunk(ctx, "gen_1", c); err != nil {
			t.Fatalf("PutChunk %d: %v", i, err)
		}
	}
	// A chunk in a different generation must not show up in gen_1's list.
	if err := s.PutChunk(ctx, "gen_2", domain.Chunk{ID: "chk_0", ObjectID: "ko_2", Content: []byte("y")}); err != nil {
		t.Fatalf("PutChunk gen_2: %v", err)
	}

	chunks, err := s.ListGenerationChunks(ctx, "gen_1")
	if err != nil {
		t.Fatalf("ListGenerationChunks: %v", err)
	}
	if len(chunks) != n {
		t.Errorf("ListGenerationChunks returned %d chunks, want %d", len(chunks), n)
	}
}

func TestDeleteGeneration(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		c := domain.Chunk{ID: fmt.Sprintf("chk_%d", i), ObjectID: "ko_1", Ordinal: i}
		if err := s.PutChunk(ctx, "gen_1", c); err != nil {
			t.Fatalf("PutChunk %d: %v", i, err)
		}
	}
	if err := s.PutManifest(ctx, "gen_1", []byte(`{"id":"gen_1"}`)); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	if err := s.DeleteGeneration(ctx, "gen_1"); err != nil {
		t.Fatalf("DeleteGeneration: %v", err)
	}

	chunks, err := s.ListGenerationChunks(ctx, "gen_1")
	if err != nil {
		t.Fatalf("ListGenerationChunks after delete: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("ListGenerationChunks after delete = %d chunks, want 0", len(chunks))
	}
	if _, err := s.GetManifest(ctx, "gen_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetManifest after delete: err = %v, want ErrNotFound", err)
	}
}

func TestListGenerationChunksRepeatedCallsDoNotLeak(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	if err := s.PutChunk(ctx, "gen_1", domain.Chunk{ID: "chk_1", ObjectID: "ko_1"}); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	for i := 0; i < 100; i++ {
		if _, err := s.ListGenerationChunks(ctx, "gen_1"); err != nil {
			t.Fatalf("ListGenerationChunks iteration %d: %v", i, err)
		}
	}
}

func TestContentHashIndex(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	if _, err := s.GetContentHashIndex(ctx, "abc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetContentHashIndex before Put: err = %v, want ErrNotFound", err)
	}
	if err := s.PutContentHashIndex(ctx, "abc", "ko_1"); err != nil {
		t.Fatalf("PutContentHashIndex: %v", err)
	}
	got, err := s.GetContentHashIndex(ctx, "abc")
	if err != nil {
		t.Fatalf("GetContentHashIndex: %v", err)
	}
	if got != "ko_1" {
		t.Errorf("GetContentHashIndex = %q, want ko_1", got)
	}
}

func TestManifestRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	if err := s.PutManifest(ctx, "gen_1", []byte(`{"object_count":0}`)); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}
	got, err := s.GetManifest(ctx, "gen_1")
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	if string(got) != `{"object_count":0}` {
		t.Errorf("GetManifest = %s, want {\"object_count\":0}", got)
	}
}

func TestEmbeddingMetadataRoundTrip(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	key := "gen_1/ollama/chk_1"
	if err := s.PutEmbeddingMetadata(ctx, key, []byte(`{"dims":768}`)); err != nil {
		t.Fatalf("PutEmbeddingMetadata: %v", err)
	}
	got, err := s.GetEmbeddingMetadata(ctx, key)
	if err != nil {
		t.Fatalf("GetEmbeddingMetadata: %v", err)
	}
	if string(got) != `{"dims":768}` {
		t.Errorf("GetEmbeddingMetadata = %s, want {\"dims\":768}", got)
	}
}

// TestChunkIdenticalToObjectStoresTextOnce: a chunk whose text equals its
// parent object's keeps no copy of it, and reads still return the text —
// through GetChunk and ListGenerationChunks alike.
func TestChunkIdenticalToObjectStoresTextOnce(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	text := bytes.Repeat([]byte("doc comment text "), 200)
	obj := domain.KnowledgeObject{ID: "ko_1", Content: text}
	chunk := domain.Chunk{ID: "chk_1", ObjectID: "ko_1", Content: text}

	b := s.NewBatch()
	if err := b.PutKnowledgeObject(obj); err != nil {
		t.Fatalf("PutKnowledgeObject: %v", err)
	}
	if err := b.PutChunk("gen_1", chunk, obj.Content); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	if err := b.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	raw, err := rawValue(s, chunkKey("gen_1", "chk_1"))
	if err != nil {
		t.Fatalf("raw chunk: %v", err)
	}
	if len(raw) > 512 {
		t.Errorf("stored chunk record is %d bytes, want it small (text kept only on the object)", len(raw))
	}

	got, err := s.GetChunk(ctx, "gen_1", "chk_1")
	if err != nil {
		t.Fatalf("GetChunk: %v", err)
	}
	if !bytes.Equal(got.Content, text) {
		t.Errorf("GetChunk content = %d bytes, want the original %d", len(got.Content), len(text))
	}
	list, err := s.ListGenerationChunks(ctx, "gen_1")
	if err != nil || len(list) != 1 || !bytes.Equal(list[0].Content, text) {
		t.Errorf("ListGenerationChunks = %v (err %v), want the chunk with its text", len(list), err)
	}
}

// TestChunkDifferingFromObjectKeepsItsOwnText: a markdown-style chunk
// (a piece of the object, not all of it) is stored whole.
func TestChunkDifferingFromObjectKeepsItsOwnText(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	b := s.NewBatch()
	obj := domain.KnowledgeObject{ID: "ko_2", Content: []byte("# A\nbody a\n# B\nbody b")}
	chunk := domain.Chunk{ID: "chk_2", ObjectID: "ko_2", Content: []byte("# A\nbody a")}
	if err := b.PutKnowledgeObject(obj); err != nil {
		t.Fatal(err)
	}
	if err := b.PutChunk("gen_1", chunk, obj.Content); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetChunk(ctx, "gen_1", "chk_2")
	if err != nil || string(got.Content) != "# A\nbody a" {
		t.Errorf("GetChunk = %q (err %v), want its own text", got.Content, err)
	}
}

// TestChunkWithMissingParentObjectFailsLoudly: a chunk that relies on its
// object for text must not silently come back empty.
func TestChunkWithMissingParentObjectFailsLoudly(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	b := s.NewBatch()
	text := []byte("same text")
	if err := b.PutChunk("gen_1", domain.Chunk{ID: "chk_3", ObjectID: "ko_missing", Content: text}, text); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChunk(ctx, "gen_1", "chk_3"); err == nil {
		t.Error("GetChunk succeeded with no parent object, want an error")
	}
}

// TestBatchWritesInvisibleUntilFlushAndDiscardedByCancel.
func TestBatchWritesInvisibleUntilFlushAndDiscardedByCancel(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	b := s.NewBatch()
	if err := b.PutKnowledgeObject(domain.KnowledgeObject{ID: "ko_b", Content: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetKnowledgeObject(ctx, "ko_b"); !errors.Is(err, ErrNotFound) {
		t.Errorf("before Flush: err = %v, want ErrNotFound", err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	b.Cancel() // safe after Flush
	if _, err := s.GetKnowledgeObject(ctx, "ko_b"); err != nil {
		t.Errorf("after Flush: %v", err)
	}

	c := s.NewBatch()
	if err := c.PutKnowledgeObject(domain.KnowledgeObject{ID: "ko_c", Content: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	c.Cancel()
	if _, err := s.GetKnowledgeObject(ctx, "ko_c"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after Cancel: err = %v, want ErrNotFound", err)
	}
}

// TestLargeValuesRoundTripAndSurviveValueLogReclaim: values above the
// value threshold live in the value log; deleting a generation and
// reclaiming must not disturb another generation's data.
func TestLargeValuesRoundTripAndSurviveValueLogReclaim(t *testing.T) {
	s, _ := openTestStore(t)
	ctx := context.Background()

	big := bytes.Repeat([]byte("v"), 200<<10)
	for _, gen := range []string{"gen_a", "gen_b"} {
		for i := 0; i < 20; i++ {
			c := domain.Chunk{ID: fmt.Sprintf("chk_%d", i), ObjectID: "ko", Content: big}
			if err := s.PutChunk(ctx, gen, c); err != nil {
				t.Fatalf("PutChunk: %v", err)
			}
		}
	}
	if err := s.DeleteGeneration(ctx, "gen_a"); err != nil {
		t.Fatalf("DeleteGeneration: %v", err)
	}
	if err := s.ReclaimValueLog(); err != nil {
		t.Fatalf("ReclaimValueLog: %v", err)
	}
	got, err := s.ListGenerationChunks(ctx, "gen_b")
	if err != nil || len(got) != 20 || !bytes.Equal(got[0].Content, big) {
		t.Errorf("gen_b after reclaim: %d chunks (err %v), want 20 intact", len(got), err)
	}
	if left, _ := s.ListGenerationChunks(ctx, "gen_a"); len(left) != 0 {
		t.Errorf("gen_a still has %d chunks after delete", len(left))
	}
}

func rawValue(s *Store, key []byte) ([]byte, error) {
	var out []byte
	err := s.db.View(func(txn *bg.Txn) error {
		item, err := txn.Get(key)
		if err != nil {
			return err
		}
		out, err = item.ValueCopy(nil)
		return err
	})
	return out, err
}
