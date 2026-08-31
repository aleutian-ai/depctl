package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureMirrorCreatesBareMirrorAtConventionPath(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	cacheRoot := t.TempDir()
	c := NewCache(cacheRoot)

	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}

	wantSuffix := filepath.Join("local", strings.TrimPrefix(filepath.ToSlash(remote), "/")+".git")
	if !strings.HasSuffix(filepath.ToSlash(repoPath), wantSuffix) {
		t.Errorf("repoPath = %s, want suffix %s (host/org/repo.git convention)", repoPath, wantSuffix)
	}
	if info, err := os.Stat(repoPath); err != nil || !info.IsDir() {
		t.Fatalf("mirror not created at %s: %v", repoPath, err)
	}
}

func TestEnsureMirrorIsNoOpWhenAlreadyPresent(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	c := NewCache(t.TempDir())

	first, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("first EnsureMirror: %v", err)
	}

	// Add a new commit to the "remote" after the first mirror — a no-op
	// EnsureMirror must not re-clone (and so must not pick this up; that's
	// FetchTags's job).
	writeFileT(t, remote, "second.txt", "second\n")
	runGit(t, remote, "add", ".")
	runGit(t, remote, "commit", "-q", "-m", "second")

	second, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("second EnsureMirror: %v", err)
	}
	if first != second {
		t.Fatalf("EnsureMirror path changed: %s vs %s", first, second)
	}

	if _, err := c.ResolveRef(context.Background(), second, "main"); err != nil {
		t.Fatalf("ResolveRef main: %v", err)
	}
}

func TestFetchTagsIncrementalUpdate(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}

	if _, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0"); err != nil {
		t.Fatalf("ResolveRef v1.0.0 before new tag: %v", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "v2.0.0"); err == nil {
		t.Fatal("expected v2.0.0 to be unresolvable before it exists")
	}

	writeFileT(t, remote, "second.txt", "second\n")
	runGit(t, remote, "add", ".")
	runGit(t, remote, "commit", "-q", "-m", "second")
	runGit(t, remote, "tag", "v2.0.0")

	if err := c.FetchTags(context.Background(), repoPath); err != nil {
		t.Fatalf("FetchTags: %v", err)
	}

	if _, err := c.ResolveRef(context.Background(), repoPath, "v2.0.0"); err != nil {
		t.Fatalf("ResolveRef v2.0.0 after FetchTags: %v", err)
	}
}

func TestResolveRefWorksOfflineAfterFetch(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")
	head := strings.TrimSpace(runGit(t, remote, "rev-parse", "HEAD"))

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if err := c.FetchTags(context.Background(), repoPath); err != nil {
		t.Fatalf("FetchTags: %v", err)
	}

	commit, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if commit != head {
		t.Errorf("ResolveRef v1.0.0 = %s, want %s", commit, head)
	}
}

func TestResolveRefUnresolvableTagIsPermanentError(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}

	_, err = c.ResolveRef(context.Background(), repoPath, "does-not-exist")
	if err == nil {
		t.Fatal("expected error for unresolvable ref")
	}
	var cacheErr *CacheError
	if !errors.As(err, &cacheErr) {
		t.Fatalf("error is not a *CacheError: %v", err)
	}
	if cacheErr.Kind != ErrKindPermanent {
		t.Errorf("Kind = %s, want permanent", cacheErr.Kind)
	}
}

func TestEnsureMirrorUnreachableRemoteIsTransientError(t *testing.T) {
	requireGit(t)

	c := NewCache(t.TempDir())
	_, err := c.EnsureMirror(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected error cloning a nonexistent remote")
	}
	var cacheErr *CacheError
	if !errors.As(err, &cacheErr) {
		t.Fatalf("error is not a *CacheError: %v", err)
	}
	if cacheErr.Kind != ErrKindTransient {
		t.Errorf("Kind = %s, want transient", cacheErr.Kind)
	}
}
