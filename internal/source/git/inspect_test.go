package git

import (
	"context"
	"testing"
)

func TestListFilesFindsEveryMatchingBasename(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	remote := newRemoteFixture(t)
	writeFileT(t, remote, "api/package.json", `{"name": "@scope/api"}`)
	writeFileT(t, remote, "core/package.json", `{"name": "@scope/core"}`)
	writeFileT(t, remote, "api/README.md", "docs\n")
	runGit(t, remote, "add", ".")
	runGit(t, remote, "commit", "-q", "-m", "add packages")

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(ctx, remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	commit, err := c.ResolveRef(ctx, repoPath, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	got, err := c.ListFiles(ctx, repoPath, commit, "package.json")
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	want := map[string]bool{"api/package.json": true, "core/package.json": true}
	if len(got) != len(want) {
		t.Fatalf("ListFiles = %v, want %v entries", got, want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("ListFiles returned unexpected path %q", p)
		}
	}
}

func TestReadFileReturnsBlobContentAtCommit(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	remote := newRemoteFixture(t)
	writeFileT(t, remote, "api/package.json", `{"name": "@scope/api"}`)
	runGit(t, remote, "add", ".")
	runGit(t, remote, "commit", "-q", "-m", "add package.json")

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(ctx, remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	commit, err := c.ResolveRef(ctx, repoPath, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	got, err := c.ReadFile(ctx, repoPath, commit, "api/package.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != `{"name": "@scope/api"}` {
		t.Errorf("ReadFile = %q, want the exact blob content", got)
	}

	if _, err := c.ReadFile(ctx, repoPath, commit, "does/not/exist.json"); err == nil {
		t.Error("ReadFile on a missing path = nil error, want an error")
	}
}
