package markdown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func TestSupportsMdAndMdx(t *testing.T) {
	n := New()
	if !n.Supports(domain.SourceSnapshot{LogicalPath: "README.md"}) {
		t.Error("Supports(.md) = false, want true")
	}
	if !n.Supports(domain.SourceSnapshot{LogicalPath: "guide.mdx"}) {
		t.Error("Supports(.mdx) = false, want true")
	}
	if n.Supports(domain.SourceSnapshot{LogicalPath: "notes.txt"}) {
		t.Error("Supports(.txt) = true, want false")
	}
}

func normalizeFixture(t *testing.T, filename, logicalPath string) domain.KnowledgeObject {
	t.Helper()
	path := filepath.Join("testdata", "sources", "markdown", filename)
	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: logicalPath})
	if err != nil {
		t.Fatalf("Normalize(%s): %v", filename, err)
	}
	if len(got) != 1 {
		t.Fatalf("Normalize(%s) returned %d objects, want 1", filename, len(got))
	}
	return got[0]
}

func TestSimpleReadmeTitleAndHeadings(t *testing.T) {
	obj := normalizeFixture(t, "simple.md", "README.md")
	if obj.Title != "My Project" {
		t.Errorf("Title = %q, want \"My Project\"", obj.Title)
	}
	want := "My Project|My Project > Installation"
	if obj.Metadata["headings"] != want {
		t.Errorf("headings = %q, want %q", obj.Metadata["headings"], want)
	}
}

func TestNestedHeadingsPreserveHierarchy(t *testing.T) {
	obj := normalizeFixture(t, "nested.md", "docs/nested.md")
	want := "Docs|Docs > Getting Started|Docs > Getting Started > Installation|Docs > Getting Started > Configuration|Docs > Reference"
	if obj.Metadata["headings"] != want {
		t.Errorf("headings = %q, want %q", obj.Metadata["headings"], want)
	}
}

func TestFencedCodeLanguagesAndLinks(t *testing.T) {
	obj := normalizeFixture(t, "code.md", "examples.md")
	if obj.Metadata["code_languages"] != "go,python" {
		t.Errorf("code_languages = %q, want \"go,python\"", obj.Metadata["code_languages"])
	}
	links := strings.Split(obj.Metadata["links"], ",")
	wantLinks := map[string]bool{"https://example.com/docs": true, "https://example.com/auto": true}
	if len(links) != 2 {
		t.Fatalf("links = %v, want 2 entries", links)
	}
	for _, l := range links {
		if !wantLinks[l] {
			t.Errorf("unexpected link %q", l)
		}
	}
}

// goldenView is the subset of a normalized KnowledgeObject compared
// against testdata/golden fixtures — Content is excluded since it's
// trivially the source bytes and not what a parser-drift regression
// would change.
type goldenView struct {
	Title       string            `json:"title"`
	ContentType string            `json:"content_type"`
	LogicalPath string            `json:"logical_path"`
	Metadata    map[string]string `json:"metadata"`
}

func TestNestedHeadingsGoldenSnapshot(t *testing.T) {
	obj := normalizeFixture(t, "nested.md", "docs/nested.md")
	got := goldenView{
		Title:       obj.Title,
		ContentType: obj.ContentType,
		LogicalPath: obj.LogicalPath,
		Metadata:    obj.Metadata,
	}

	goldenBytes, err := os.ReadFile(filepath.Join("testdata", "golden", "nested.golden.json"))
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var want goldenView
	if err := json.Unmarshal(goldenBytes, &want); err != nil {
		t.Fatalf("unmarshal golden fixture: %v", err)
	}

	gotJSON, _ := json.MarshalIndent(got, "", "  ")
	wantJSON, _ := json.MarshalIndent(want, "", "  ")
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("normalized output drifted from golden fixture:\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

// TestHeadingBeforeH1DoesNotStealTitle reproduces a real-world bug found
// normalizing spf13/cobra's actual README: an H3 sponsor callout inside
// an HTML block appears before the document's real "# Overview" H1. The
// level-skip clamp (needed so an out-of-order heading doesn't index out
// of range on the breadcrumb stack) was also feeding title detection,
// so the H3 got silently promoted to "the title" instead of Overview.
func TestHeadingBeforeH1DoesNotStealTitle(t *testing.T) {
	obj := normalizeFixture(t, "skewed-heading.md", "README.md")
	if obj.Title != "Overview" {
		t.Errorf("Title = %q, want \"Overview\" (the real H1, not the earlier out-of-order H3)", obj.Title)
	}
}

func TestEmptyDocumentGetsParseWarning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.md")
	if err := os.WriteFile(path, []byte("   \n\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: "empty.md"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got[0].Metadata["parse_warning"] != "true" {
		t.Errorf("parse_warning = %q, want \"true\" for an empty document", got[0].Metadata["parse_warning"])
	}
}

func TestMdxTreatedAsPlainMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "guide.mdx")
	content := "# Guide\n\n<CustomComponent prop=\"value\" />\n\nSome prose.\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: "guide.mdx"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got[0].Title != "Guide" {
		t.Errorf("Title = %q, want \"Guide\"", got[0].Title)
	}
}
