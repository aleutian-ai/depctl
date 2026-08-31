package markdown

import (
	"context"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestSmallSectionUnderLimitProducesOneChunk(t *testing.T) {
	obj := domain.KnowledgeObject{ID: "ko_1", Content: []byte("# Title\n\nShort body.\n")}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Metadata["heading_path"] != "Title" {
		t.Errorf("heading_path = %q, want Title", got[0].Metadata["heading_path"])
	}
}

func TestOversizedSectionSplitsAtParagraphBoundaries(t *testing.T) {
	para := strings.Repeat("word ", 50) // ~250 bytes
	content := "# Big Section\n\n" + para + "\n\n" + para + "\n\n" + para + "\n\n" + para + "\n"
	obj := domain.KnowledgeObject{ID: "ko_2", Content: []byte(content)}

	got, err := New(300).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("got %d chunks, want the oversized section split into multiple", len(got))
	}
	for i, c := range got {
		if len(c.Content) > 300 {
			t.Errorf("chunk %d is %d bytes, want <= 300", i, len(c.Content))
		}
		if c.Metadata["heading_path"] != "Big Section" {
			t.Errorf("chunk %d heading_path = %q, want \"Big Section\"", i, c.Metadata["heading_path"])
		}
	}
}

func TestSingleOversizedParagraphAllowedToExceedLimit(t *testing.T) {
	// Documented accepted edge case: a lone paragraph bigger than the
	// limit is never cut mid-paragraph.
	huge := strings.Repeat("x", 5000)
	content := "# Title\n\n" + huge + "\n"
	obj := domain.KnowledgeObject{ID: "ko_3", Content: []byte(content)}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1 (single oversized paragraph, not split)", len(got))
	}
	if len(got[0].Content) < 5000 {
		t.Errorf("chunk content len = %d, want the full oversized paragraph preserved", len(got[0].Content))
	}
}

func TestHeadingPathThreadedThroughNestedHeadings(t *testing.T) {
	content := "# Docs\n\n## Getting Started\n\n### Installation\n\nInstall steps.\n\n## Reference\n\nAPI reference.\n"
	obj := domain.KnowledgeObject{ID: "ko_4", Content: []byte(content)}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}

	paths := make([]string, len(got))
	for i, c := range got {
		paths[i] = c.Metadata["heading_path"]
	}
	want := []string{"Docs", "Docs > Getting Started", "Docs > Getting Started > Installation", "Docs > Reference"}
	if len(paths) != len(want) {
		t.Fatalf("heading paths = %v, want %v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Errorf("path[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestFencedCodeHashNotMistakenForHeading(t *testing.T) {
	content := "# Title\n\nSome intro.\n\n```bash\n# this is a shell comment, not a heading\necho hi\n```\n\nMore text.\n"
	obj := domain.KnowledgeObject{ID: "ko_5", Content: []byte(content)}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1 (the fenced '#' line must not split the section)", len(got))
	}
	if !strings.Contains(string(got[0].Content), "# this is a shell comment") {
		t.Error("fenced code content was lost or mangled")
	}
}

func TestChunkIDsStableAcrossRepeatedChunking(t *testing.T) {
	obj := domain.KnowledgeObject{ID: "ko_6", Content: []byte("# T\n\nbody\n")}
	a, _ := New(2000).Chunk(context.Background(), obj)
	b, _ := New(2000).Chunk(context.Background(), obj)
	if len(a) != len(b) || a[0].ID != b[0].ID {
		t.Errorf("chunk IDs not stable: %v vs %v", a, b)
	}
}

func TestNoHeadingsTreatedAsOneSection(t *testing.T) {
	obj := domain.KnowledgeObject{ID: "ko_7", Content: []byte("Just plain text content, no headings at all.\n")}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Metadata["heading_path"] != "" {
		t.Errorf("heading_path = %q, want empty for headless content", got[0].Metadata["heading_path"])
	}
}

func TestDefaultMaxChunkBytesUsedWhenNonPositive(t *testing.T) {
	c := New(0)
	if c.maxChunkBytes != DefaultMaxChunkBytes {
		t.Errorf("maxChunkBytes = %d, want default %d", c.maxChunkBytes, DefaultMaxChunkBytes)
	}
}
