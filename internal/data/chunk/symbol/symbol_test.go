package symbol

import (
	"context"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func TestExportedFunctionSymbolProducesOneChunk(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          "ko_abc123",
		ContentType: "symbol_doc",
		LogicalPath: "cobra",
		Version:     "v1.8.0",
		Content:     []byte("Greet returns a friendly greeting for name."),
		Metadata: map[string]string{
			"package":   "cobra",
			"symbol":    "Greet",
			"signature": "func Greet(name string) string",
		},
	}

	got, err := New().Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}

	c := got[0]
	if c.ObjectID != "ko_abc123" {
		t.Errorf("ObjectID = %q, want ko_abc123", c.ObjectID)
	}
	if c.Metadata["package"] != "cobra" || c.Metadata["symbol"] != "Greet" || c.Metadata["signature"] != "func Greet(name string) string" {
		t.Errorf("Metadata = %+v, missing expected package/symbol/signature", c.Metadata)
	}
	if c.Metadata["source_path"] != "cobra" {
		t.Errorf("source_path = %q, want cobra (from obj.LogicalPath)", c.Metadata["source_path"])
	}
	if c.Metadata["version"] != "v1.8.0" {
		t.Errorf("version = %q, want v1.8.0", c.Metadata["version"])
	}
	if string(c.Content) != string(obj.Content) {
		t.Errorf("Content = %q, want passthrough of obj.Content", c.Content)
	}
}

// TestDependencyAndSourceTypePromotedToChunkMetadata is STRUCT-003's own
// acceptance criterion: dependency/source_type sit on the parent
// KnowledgeObject only, so a caller holding just a Chunk previously
// needed a second lookup to know either — both must now be promoted
// directly onto Chunk.Metadata.
func TestDependencyAndSourceTypePromotedToChunkMetadata(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          "ko_dep",
		ContentType: "symbol_doc",
		LogicalPath: "cobra",
		Version:     "v1.8.0",
		SourceType:  "godoc",
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Name: "github.com/spf13/cobra"},
		},
		Content: []byte("Greet returns a friendly greeting for name."),
		Metadata: map[string]string{
			"package":   "cobra",
			"symbol":    "Greet",
			"signature": "func Greet(name string) string",
		},
	}

	got, err := New().Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if got[0].Metadata["dependency"] != "github.com/spf13/cobra" {
		t.Errorf("dependency = %q, want github.com/spf13/cobra", got[0].Metadata["dependency"])
	}
	if got[0].Metadata["source_type"] != "godoc" {
		t.Errorf("source_type = %q, want godoc", got[0].Metadata["source_type"])
	}
}

// TestEmptyDependencyAndSourceTypeStillPresentNotOmitted covers the
// ticket's edge case: an obj with no Dependency/SourceType set still
// produces present-but-empty metadata keys, not an error or an omitted
// key — Chunk never errors on missing identity fields.
func TestEmptyDependencyAndSourceTypeStillPresentNotOmitted(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:      "ko_noDep",
		Content: []byte("body"),
		Metadata: map[string]string{
			"package": "cobra",
			"symbol":  "Greet",
		},
	}

	got, err := New().Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if v, ok := got[0].Metadata["dependency"]; !ok || v != "" {
		t.Errorf("dependency = %q, ok=%v, want present and empty", v, ok)
	}
	if v, ok := got[0].Metadata["source_type"]; !ok || v != "" {
		t.Errorf("source_type = %q, ok=%v, want present and empty", v, ok)
	}
}

func TestPackageDocObjectHandledDistinctlyFromSymbol(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          "ko_pkgdoc",
		ContentType: "package_doc",
		LogicalPath: "cobra",
		Content:     []byte("Package cobra is a commander..."),
		Metadata:    map[string]string{"package": "cobra"},
	}

	got, err := New().Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Metadata["symbol"] != "" {
		t.Errorf("symbol = %q, want empty for a package-level doc object", got[0].Metadata["symbol"])
	}
	if got[0].Metadata["package"] != "cobra" {
		t.Errorf("package = %q, want cobra", got[0].Metadata["package"])
	}
}

// TestUndocumentedSymbolFallsBackToSignature reproduces a real-world
// finding from hack/chunk-sweep against ~21k real Go symbol objects:
// ~11% of exported symbols have no doc comment at all, which NORM-004
// correctly represents as empty Content — but that meant CHUNK-003 was
// emitting a completely empty chunk (zero signal for embedding/
// retrieval) even though the signature, a genuinely useful piece of
// information, was sitting right there in metadata, unused.
func TestUndocumentedSymbolFallsBackToSignature(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          "ko_undoc",
		ContentType: "symbol_doc",
		LogicalPath: "pkg/common",
		Content:     nil, // no doc comment
		Metadata: map[string]string{
			"package":   "common",
			"symbol":    "CreateAuthorizationToken",
			"signature": "func CreateAuthorizationToken(taskID, runID, jobID int64) (string, error)",
		},
	}

	got, err := New().Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got[0].Content) == 0 {
		t.Fatal("Content is empty, want a fallback to the signature")
	}
	if string(got[0].Content) != obj.Metadata["signature"] {
		t.Errorf("Content = %q, want the signature %q", got[0].Content, obj.Metadata["signature"])
	}
	// Metadata itself is untouched — the fallback only affects Content.
	if got[0].Metadata["signature"] != obj.Metadata["signature"] {
		t.Errorf("signature metadata changed: %q", got[0].Metadata["signature"])
	}
}

func TestUndocumentedSymbolWithNoSignatureFallsBackToPackageDotSymbol(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:          "ko_noSig",
		ContentType: "symbol_doc",
		Content:     nil,
		Metadata:    map[string]string{"package": "common", "symbol": "someConst"},
	}

	got, err := New().Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if string(got[0].Content) != "common.someConst" {
		t.Errorf("Content = %q, want \"common.someConst\"", got[0].Content)
	}
}

func TestChunkIDStableAcrossRepeatedChunking(t *testing.T) {
	obj := domain.KnowledgeObject{ID: "ko_x", Content: []byte("same content"), Metadata: map[string]string{}}
	a, _ := New().Chunk(context.Background(), obj)
	b, _ := New().Chunk(context.Background(), obj)
	if a[0].ID != b[0].ID {
		t.Errorf("ChunkID not stable: %s != %s", a[0].ID, b[0].ID)
	}
}
