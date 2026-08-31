package releasenotes

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
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

func TestSupportsKnownFilenames(t *testing.T) {
	n := New()
	for _, name := range []string{"CHANGELOG.md", "changelog.md", "CHANGES.md", "RELEASES.md", "HISTORY.md", "HISTORY.txt"} {
		if !n.Supports(domain.SourceSnapshot{LogicalPath: name}) {
			t.Errorf("Supports(%s) = false, want true", name)
		}
	}
	if n.Supports(domain.SourceSnapshot{LogicalPath: "README.md"}) {
		t.Error("Supports(README.md) = true, want false")
	}
}

func TestSupportsGithubReleasesHint(t *testing.T) {
	n := New()
	src := domain.SourceSnapshot{LogicalPath: "release-v1.2.3", Metadata: map[string]string{"source_type": "github-releases"}}
	if !n.Supports(src) {
		t.Error("Supports(github-releases hint) = false, want true")
	}
}

func TestChangelogVersionedHeadingsTagged(t *testing.T) {
	content := "# Changelog\n\n## v1.2.0\n\nBug fixes.\n\n## v1.1.0\n\nInitial release.\n"
	path := writeFixture(t, "CHANGELOG.md", content)

	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: "CHANGELOG.md"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1 (markdown normalizer emits one per document)", len(got))
	}

	obj := got[0]
	if obj.Metadata["content_type"] != "release_note" {
		t.Errorf("content_type = %q, want release_note", obj.Metadata["content_type"])
	}
	if obj.Metadata["release_version"] != "v1.2.0" {
		t.Errorf("release_version = %q, want v1.2.0 (the first/newest version heading)", obj.Metadata["release_version"])
	}
	if obj.Metadata["headings"] == "" {
		t.Error("headings metadata missing — needed for later per-section version tagging in CHUNK-002")
	}
}

func TestPlainTextReleaseNotesTaggedWithoutVersionSections(t *testing.T) {
	path := writeFixture(t, "HISTORY.txt", "1.0.0 - initial release\n")

	n := New()
	got, err := n.Normalize(context.Background(), domain.SourceSnapshot{LocalPath: path, LogicalPath: "HISTORY.txt"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d objects, want 1", len(got))
	}
	if got[0].Metadata["content_type"] != "release_note" {
		t.Errorf("content_type = %q, want release_note", got[0].Metadata["content_type"])
	}
	if _, ok := got[0].Metadata["release_version"]; ok {
		t.Error("plain-text delegate should not produce release_version (best-effort, no heading structure)")
	}
}
