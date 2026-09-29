package markdown

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

// sectionPathOf unmarshals a chunk's Metadata["section_path"] (STRUCT-001)
// back into a []string for assertions, failing the test on invalid JSON.
func sectionPathOf(t *testing.T, c domain.Chunk) []string {
	t.Helper()
	var path []string
	if err := json.Unmarshal([]byte(c.Metadata["section_path"]), &path); err != nil {
		t.Fatalf("section_path %q is not valid JSON: %v", c.Metadata["section_path"], err)
	}
	return path
}

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
	if want := []string{"Title"}; !reflect.DeepEqual(sectionPathOf(t, got[0]), want) {
		t.Errorf("section_path = %v, want %v", sectionPathOf(t, got[0]), want)
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
	want := []string{"Big Section"}
	for i, c := range got {
		if len(c.Content) > 300 {
			t.Errorf("chunk %d is %d bytes, want <= 300", i, len(c.Content))
		}
		if c.Metadata["heading_path"] != "Big Section" {
			t.Errorf("chunk %d heading_path = %q, want \"Big Section\"", i, c.Metadata["heading_path"])
		}
		// STRUCT-001: every part of a section split by packSection's
		// paragraph-fallback carries the identical section_path — it
		// identifies which section a chunk belongs to, not which
		// byte-range within it.
		if got := sectionPathOf(t, c); !reflect.DeepEqual(got, want) {
			t.Errorf("chunk %d section_path = %v, want %v", i, got, want)
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

	// STRUCT-001: section_path is the same ancestor path, unjoined —
	// both derived from one headingStack snapshot per section.
	wantSectionPaths := [][]string{
		{"Docs"},
		{"Docs", "Getting Started"},
		{"Docs", "Getting Started", "Installation"},
		{"Docs", "Reference"},
	}
	for i, w := range wantSectionPaths {
		if got := sectionPathOf(t, got[i]); !reflect.DeepEqual(got, w) {
			t.Errorf("section_path[%d] = %v, want %v", i, got, w)
		}
	}
}

// TestSectionPathReflectsLevelSkipClamping covers a heading that jumps
// more than one level deeper than its parent (H1 straight to H3) —
// section_path must reflect the same one-deeper-than-stack clamping
// headingStack already applies for heading_path, not a literal
// three-deep path with a missing H2.
func TestSectionPathReflectsLevelSkipClamping(t *testing.T) {
	content := "# Docs\n\n### Deep\n\nContent.\n"
	obj := domain.KnowledgeObject{ID: "ko_skip", Content: []byte(content)}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2 (Docs, Deep)", len(got))
	}
	if got[1].Metadata["heading_path"] != "Docs > Deep" {
		t.Errorf("heading_path = %q, want \"Docs > Deep\" (clamped one level deeper, not three)", got[1].Metadata["heading_path"])
	}
	want := []string{"Docs", "Deep"}
	if got := sectionPathOf(t, got[1]); !reflect.DeepEqual(got, want) {
		t.Errorf("section_path = %v, want %v (clamped, matching heading_path's own clamping)", got, want)
	}
}

// TestDependencyVersionSourceTypePromotedToChunkMetadata is STRUCT-003's
// own acceptance criterion for the markdown chunker: dependency, version,
// and source_type sit on the parent KnowledgeObject only, so a caller
// holding just a Chunk previously needed a second lookup to know any of
// them — all three must now be promoted directly onto Chunk.Metadata.
func TestDependencyVersionSourceTypePromotedToChunkMetadata(t *testing.T) {
	obj := domain.KnowledgeObject{
		ID:         "ko_dep",
		Version:    "v1.8.0",
		SourceType: "markdown",
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Name: "github.com/spf13/cobra"},
		},
		Content: []byte("# Title\n\nShort body.\n"),
	}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if got[0].Metadata["dependency"] != "github.com/spf13/cobra" {
		t.Errorf("dependency = %q, want github.com/spf13/cobra", got[0].Metadata["dependency"])
	}
	if got[0].Metadata["version"] != "v1.8.0" {
		t.Errorf("version = %q, want v1.8.0", got[0].Metadata["version"])
	}
	if got[0].Metadata["source_type"] != "markdown" {
		t.Errorf("source_type = %q, want markdown", got[0].Metadata["source_type"])
	}
}

// TestEmptyDependencyVersionSourceTypeStillPresentNotOmitted covers the
// ticket's edge case: an obj with no Dependency/Version/SourceType set
// still produces present-but-empty metadata keys, not an error or an
// omitted key.
func TestEmptyDependencyVersionSourceTypeStillPresentNotOmitted(t *testing.T) {
	obj := domain.KnowledgeObject{ID: "ko_noDep", Content: []byte("# Title\n\nbody\n")}

	got, err := New(2000).Chunk(context.Background(), obj)
	if err != nil {
		t.Fatalf("Chunk: %v", err)
	}
	if v, ok := got[0].Metadata["dependency"]; !ok || v != "" {
		t.Errorf("dependency = %q, ok=%v, want present and empty", v, ok)
	}
	if v, ok := got[0].Metadata["version"]; !ok || v != "" {
		t.Errorf("version = %q, ok=%v, want present and empty", v, ok)
	}
	if v, ok := got[0].Metadata["source_type"]; !ok || v != "" {
		t.Errorf("source_type = %q, ok=%v, want present and empty", v, ok)
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
	// STRUCT-001: a nil sectionPath marshals to the JSON literal "null",
	// not "[]" — matches heading_path's own empty-string "no section"
	// convention.
	if got := got[0].Metadata["section_path"]; got != "null" {
		t.Errorf("section_path = %q, want \"null\" for headless content", got)
	}
}

func TestDefaultMaxChunkBytesUsedWhenNonPositive(t *testing.T) {
	c := New(0)
	if c.maxChunkBytes != DefaultMaxChunkBytes {
		t.Errorf("maxChunkBytes = %d, want default %d", c.maxChunkBytes, DefaultMaxChunkBytes)
	}
}
