package git

import (
	"context"
	"testing"
)

func TestDeltaAddedModifiedDeleted(t *testing.T) {
	requireGit(t)

	dir := newRemoteFixture(t) // commit1: README.md
	writeFileT(t, dir, "a.txt", "a v1\n")
	writeFileT(t, dir, "b.txt", "b\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "add a and b")
	c1 := trimmed(runGit(t, dir, "rev-parse", "HEAD"))

	writeFileT(t, dir, "a.txt", "a v2, changed content\n")
	runGit(t, dir, "rm", "-q", "b.txt")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "modify a, delete b")
	c2 := trimmed(runGit(t, dir, "rev-parse", "HEAD"))

	c := NewCache(t.TempDir())
	deltas, err := c.Delta(context.Background(), dir, c1, c2)
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}

	got := map[string]FileStatus{}
	for _, d := range deltas {
		got[d.Path] = d.Status
	}
	if got["a.txt"] != FileModified {
		t.Errorf("a.txt status = %s, want modified", got["a.txt"])
	}
	if got["b.txt"] != FileDeleted {
		t.Errorf("b.txt status = %s, want deleted", got["b.txt"])
	}
}

func TestDeltaRenamed(t *testing.T) {
	requireGit(t)

	dir := newRemoteFixture(t)
	content := "identical content across the rename, long enough for git's similarity heuristic to detect it as a rename rather than a delete+add pair.\n"
	writeFileT(t, dir, "c.txt", content)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "add c")
	c1 := trimmed(runGit(t, dir, "rev-parse", "HEAD"))

	runGit(t, dir, "mv", "c.txt", "d.txt")
	runGit(t, dir, "commit", "-q", "-m", "rename c to d")
	c2 := trimmed(runGit(t, dir, "rev-parse", "HEAD"))

	c := NewCache(t.TempDir())
	deltas, err := c.Delta(context.Background(), dir, c1, c2)
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}

	var found *FileDelta
	for i := range deltas {
		if deltas[i].Status == FileRenamed {
			found = &deltas[i]
		}
	}
	if found == nil {
		t.Fatalf("no rename detected in %+v", deltas)
	}
	if found.OldPath != "c.txt" || found.Path != "d.txt" {
		t.Errorf("rename = %+v, want OldPath=c.txt Path=d.txt", found)
	}
}

func TestDeltaFirstSyncAgainstEmptyTree(t *testing.T) {
	requireGit(t)

	dir := newRemoteFixture(t)
	writeFileT(t, dir, "a.txt", "a\n")
	writeFileT(t, dir, "b.txt", "b\n")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-q", "-m", "add a and b")
	head := trimmed(runGit(t, dir, "rev-parse", "HEAD"))

	c := NewCache(t.TempDir())
	deltas, err := c.Delta(context.Background(), dir, "", head)
	if err != nil {
		t.Fatalf("Delta: %v", err)
	}

	got := map[string]FileStatus{}
	for _, d := range deltas {
		got[d.Path] = d.Status
	}
	for _, path := range []string{"README.md", "a.txt", "b.txt"} {
		if got[path] != FileAdded {
			t.Errorf("%s status = %s, want added", path, got[path])
		}
	}
}

func trimmed(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
