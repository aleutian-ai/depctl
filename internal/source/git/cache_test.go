package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// TestConcurrentEnsureMirrorOfSameRepoDoesNotCorruptMirror is a
// regression test caught by an independent adversarial review: nothing
// previously serialized two concurrent first-time EnsureMirror calls for
// the same repository URL, and the mirror directory is a resource
// shared across every generation of that dependency (not scoped to one
// build) — a corrupted mirror from a racing `git clone --mirror` would
// silently break every future build of that dependency, not just the
// racing calls. Runs many concurrent EnsureMirror calls against the
// same fixture repo and confirms every one succeeds with an identical,
// usable mirror.
func TestConcurrentEnsureMirrorOfSameRepoDoesNotCorruptMirror(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	c := NewCache(t.TempDir())

	const n = 8
	var wg sync.WaitGroup
	paths := make([]string, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			paths[i], errs[i] = c.EnsureMirror(context.Background(), remote)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: EnsureMirror: %v", i, err)
		}
		if paths[i] != paths[0] {
			t.Errorf("goroutine %d: repoPath = %s, want %s", i, paths[i], paths[0])
		}
	}

	// A corrupted mirror (e.g. two overlapping `git clone --mirror`
	// writes) would typically fail here, not just look present.
	if _, err := c.ResolveRef(context.Background(), paths[0], "v1.0.0"); err != nil {
		t.Fatalf("ResolveRef after concurrent EnsureMirror: %v", err)
	}
}
