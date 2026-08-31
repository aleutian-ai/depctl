package badger

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

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
