package normalize

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

// fakeNormalizer is a minimal Normalizer used to exercise the interface
// contract and Registry.Select without depending on any real
// content-type parser.
type fakeNormalizer struct {
	name    string
	ext     string
	objects []domain.KnowledgeObject
}

func (f *fakeNormalizer) Name() string    { return f.name }
func (f *fakeNormalizer) Version() string { return "v1" }

func (f *fakeNormalizer) Supports(src domain.SourceSnapshot) bool {
	return len(src.LogicalPath) >= len(f.ext) && src.LogicalPath[len(src.LogicalPath)-len(f.ext):] == f.ext
}

func (f *fakeNormalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	return f.objects, nil
}

func TestFakeNormalizerContract(t *testing.T) {
	obj := domain.KnowledgeObject{Title: "fake"}
	n := &fakeNormalizer{name: "fake-normalizer", ext: ".fake", objects: []domain.KnowledgeObject{obj}}

	src := domain.SourceSnapshot{LogicalPath: "README.fake"}
	if !n.Supports(src) {
		t.Fatal("Supports() = false, want true for .fake file")
	}

	got, err := n.Normalize(context.Background(), src)
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got) != 1 || got[0].Title != "fake" {
		t.Errorf("Normalize() = %+v, want one object titled \"fake\"", got)
	}
}

func TestRegistrySelectPicksFirstMatch(t *testing.T) {
	md := &fakeNormalizer{name: "md", ext: ".md"}
	txt := &fakeNormalizer{name: "txt", ext: ".txt"}
	reg := NewRegistry(md, txt)

	n, ok := reg.Select(domain.SourceSnapshot{LogicalPath: "README.md"})
	if !ok || n.Name() != "md" {
		t.Errorf("Select(.md) = %v, %v, want md normalizer", n, ok)
	}

	n, ok = reg.Select(domain.SourceSnapshot{LogicalPath: "NOTES.txt"})
	if !ok || n.Name() != "txt" {
		t.Errorf("Select(.txt) = %v, %v, want txt normalizer", n, ok)
	}
}

func TestRegistrySelectReturnsFalseWhenNoMatch(t *testing.T) {
	reg := NewRegistry(&fakeNormalizer{name: "md", ext: ".md"})
	_, ok := reg.Select(domain.SourceSnapshot{LogicalPath: "main.go"})
	if ok {
		t.Error("Select(main.go) = true, want false (no normalizer supports .go)")
	}
}
