package plaintext

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
	return path
}

func TestSupportsTxtAndRst(t *testing.T) {
	n := New()
	if !n.Supports(domain.SourceSnapshot{LogicalPath: "NOTES.txt"}) {
		t.Error("Supports(.txt) = false, want true")
	}
	if !n.Supports(domain.SourceSnapshot{LogicalPath: "docs/README.rst"}) {
		t.Error("Supports(.rst) = false, want true")
	}
	if n.Supports(domain.SourceSnapshot{LogicalPath: "main.go"}) {
		t.Error("Supports(.go) = true, want false")
	}
}

func TestSupportsLicenseOnlyWhenOptedIn(t *testing.T) {
	n := New()
	src := domain.SourceSnapshot{LogicalPath: "LICENSE"}
	if n.Supports(src) {
		t.Error("Supports(LICENSE) with no hint = true, want false (opt-in)")
	}

	src.Metadata = map[string]string{"include_license": "true"}
	if !n.Supports(src) {
		t.Error("Supports(LICENSE) with include_license=true = false, want true")
	}

	src2 := domain.SourceSnapshot{LogicalPath: "LICENSE.md", Metadata: map[string]string{"include_license": "true"}}
	if !n.Supports(src2) {
		t.Error("Supports(LICENSE.md) with hint = false, want true")
	}
}

func TestNormalizeTxt(t *testing.T) {
	path := writeFixture(t, "notes.txt", "  hello world  \n\n")
	n := New()

	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: "notes.txt"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1", len(got))
	}
	obj := got[0]
	if obj.Title != "notes.txt" {
		t.Errorf("Title = %q, want notes.txt", obj.Title)
	}
	if string(obj.Content) != "hello world" {
		t.Errorf("Content = %q, want trimmed \"hello world\"", obj.Content)
	}
	if obj.ContentType != "text" {
		t.Errorf("ContentType = %q, want text", obj.ContentType)
	}
}

func TestNormalizeRstAsPlainText(t *testing.T) {
	path := writeFixture(t, "readme.rst", "Title\n=====\n\nSome body text.\n")
	n := New()

	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: "readme.rst"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	// No RST directive parsing attempted — the underline markup survives
	// verbatim in Content.
	if string(got[0].Content) != "Title\n=====\n\nSome body text." {
		t.Errorf("Content = %q, want raw RST text unparsed", got[0].Content)
	}
}
