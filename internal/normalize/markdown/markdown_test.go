package markdown

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

// codeBlocksOf unmarshals a KnowledgeObject's Metadata["code_blocks"]
// (STRUCT-002) into []codeBlockRecord for assertions.
func codeBlocksOf(t *testing.T, obj domain.KnowledgeObject) []codeBlockRecord {
	t.Helper()
	var blocks []codeBlockRecord
	if err := json.Unmarshal([]byte(obj.Metadata["code_blocks"]), &blocks); err != nil {
		t.Fatalf("code_blocks %q is not valid JSON: %v", obj.Metadata["code_blocks"], err)
	}
	return blocks
}

// TestFencedCodeBlocksStructuredRecords is STRUCT-002's own acceptance
// criterion: code.md has two fenced blocks, both under the single "#
// Examples" section, in document order.
func TestFencedCodeBlocksStructuredRecords(t *testing.T) {
	obj := normalizeFixture(t, "code.md", "examples.md")
	blocks := codeBlocksOf(t, obj)
	if len(blocks) != 2 {
		t.Fatalf("code_blocks = %+v, want 2 records", blocks)
	}
	if blocks[0].Index != 0 || blocks[0].Language != "go" || !strings.Contains(blocks[0].Content, "func main()") {
		t.Errorf("blocks[0] = %+v, want index 0, language go, content containing func main()", blocks[0])
	}
	if blocks[1].Index != 1 || blocks[1].Language != "python" || !strings.Contains(blocks[1].Content, "def main():") {
		t.Errorf("blocks[1] = %+v, want index 1, language python, content containing def main():", blocks[1])
	}
	wantPath := []string{"Examples"}
	if !reflect.DeepEqual(blocks[0].SectionPath, wantPath) || !reflect.DeepEqual(blocks[1].SectionPath, wantPath) {
		t.Errorf("section_path = %v / %v, want both %v (both blocks sit under the same section)", blocks[0].SectionPath, blocks[1].SectionPath, wantPath)
	}
}

// TestFencedCodeBlocksAcrossMultipleSections covers the ticket's other
// stated case: several fenced blocks across different sections and
// nesting levels — index increments in document order, and each
// record's section_path reflects its own position, not a shared or
// last-seen one.
func TestFencedCodeBlocksAcrossMultipleSections(t *testing.T) {
	content := "# Docs\n\n## Setup\n\n```bash\nnpm install\n```\n\n## Usage\n\n### Basic\n\n```js\nrun()\n```\n\nNo-language fence:\n\n```\nplain text\n```\n"
	dir := t.TempDir()
	path := filepath.Join(dir, "multi.md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: "multi.md"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	blocks := codeBlocksOf(t, got[0])
	if len(blocks) != 3 {
		t.Fatalf("code_blocks = %+v, want 3 records", blocks)
	}

	if blocks[0].Index != 0 || blocks[0].Language != "bash" {
		t.Errorf("blocks[0] = %+v, want index 0, language bash", blocks[0])
	}
	if want := []string{"Docs", "Setup"}; !reflect.DeepEqual(blocks[0].SectionPath, want) {
		t.Errorf("blocks[0].SectionPath = %v, want %v", blocks[0].SectionPath, want)
	}

	if blocks[1].Index != 1 || blocks[1].Language != "js" {
		t.Errorf("blocks[1] = %+v, want index 1, language js", blocks[1])
	}
	if want := []string{"Docs", "Usage", "Basic"}; !reflect.DeepEqual(blocks[1].SectionPath, want) {
		t.Errorf("blocks[1].SectionPath = %v, want %v", blocks[1].SectionPath, want)
	}

	// A fence with no info string still produces a record, Language ""
	// — never dropped, and never counted into code_languages.
	if blocks[2].Index != 2 || blocks[2].Language != "" {
		t.Errorf("blocks[2] = %+v, want index 2, language \"\"", blocks[2])
	}
	if got[0].Metadata["code_languages"] != "bash,js" {
		t.Errorf("code_languages = %q, want \"bash,js\" (the no-language fence excluded)", got[0].Metadata["code_languages"])
	}
}

// TestNoCodeBlocksKeyAbsentWhenNoFencedCode is STRUCT-002's other stated
// case: nested.md has no fenced code at all, so Metadata["code_blocks"]
// must be entirely absent, matching code_languages/links' own
// omit-when-empty convention — not present-but-empty/null.
func TestNoCodeBlocksKeyAbsentWhenNoFencedCode(t *testing.T) {
	obj := normalizeFixture(t, "nested.md", "docs/nested.md")
	if _, ok := obj.Metadata["code_blocks"]; ok {
		t.Errorf("Metadata[code_blocks] = %q, want the key absent entirely for a document with no fenced code", obj.Metadata["code_blocks"])
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
