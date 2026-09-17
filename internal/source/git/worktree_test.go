package git

import (
	"context"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mirrorFixture(t *testing.T) (*Cache, string) {
	t.Helper()
	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	writeFileT(t, remote, "second.txt", "second\n")
	runGit(t, remote, "add", ".")
	runGit(t, remote, "commit", "-q", "-m", "second")
	runGit(t, remote, "tag", "v2.0.0")

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	return c, repoPath
}

func TestMaterializeWorktreeTwoVersionsSequentially(t *testing.T) {
	requireGit(t)
	c, repoPath := mirrorFixture(t)

	v1, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0")
	if err != nil {
		t.Fatalf("ResolveRef v1.0.0: %v", err)
	}
	v2, err := c.ResolveRef(context.Background(), repoPath, "v2.0.0")
	if err != nil {
		t.Fatalf("ResolveRef v2.0.0: %v", err)
	}

	dir1, cleanup1, err := c.MaterializeWorktree(context.Background(), repoPath, v1, nil)
	if err != nil {
		t.Fatalf("MaterializeWorktree v1: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir1, "second.txt")); !os.IsNotExist(err) {
		t.Errorf("v1 worktree should not contain second.txt (err=%v)", err)
	}
	if err := cleanup1(); err != nil {
		t.Fatalf("cleanup1: %v", err)
	}
	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Errorf("worktree dir %s still exists after cleanup", dir1)
	}
	assertNoWorktreeListed(t, repoPath, dir1)

	dir2, cleanup2, err := c.MaterializeWorktree(context.Background(), repoPath, v2, nil)
	if err != nil {
		t.Fatalf("MaterializeWorktree v2: %v", err)
	}
	defer cleanup2()
	if _, err := os.Stat(filepath.Join(dir2, "second.txt")); err != nil {
		t.Errorf("v2 worktree should contain second.txt: %v", err)
	}
	if dir1 == dir2 {
		t.Errorf("expected distinct worktree dirs, got the same: %s", dir1)
	}
}

func TestMaterializeWorktreeCleanupAfterCancellation(t *testing.T) {
	requireGit(t)
	c, repoPath := mirrorFixture(t)

	v1, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0")
	if err != nil {
		t.Fatalf("ResolveRef v1.0.0: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	dir, cleanup, err := c.MaterializeWorktree(ctx, repoPath, v1, nil)
	if err != nil {
		t.Fatalf("MaterializeWorktree: %v", err)
	}

	// Simulate normalization being cancelled mid-flight: cleanup must
	// still succeed because it runs on its own background context, not
	// the caller's.
	cancel()

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup after cancellation: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir %s still exists after cleanup", dir)
	}
	assertNoWorktreeListed(t, repoPath, dir)
}

func assertNoWorktreeListed(t *testing.T, repoPath, dir string) {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "worktree", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("git worktree list: %v\n%s", err, out)
	}
	if strings.Contains(string(out), dir) {
		t.Errorf("git worktree list still shows %s:\n%s", dir, out)
	}
}

// TestMaterializeWorktreeSparsePatternsFilterFiles is GIT-005's core
// regression test: a sparse-pattern worktree checks out only matching
// files, leaving everything else absent (not just unread).
func TestMaterializeWorktreeSparsePatternsFilterFiles(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t) // already has README.md
	writeFileT(t, remote, "docs/guide.md", "guide\n")
	writeFileT(t, remote, "main.go", "package main\n")
	writeFileT(t, remote, "data.bin", "binary\n")
	runGit(t, remote, "add", ".")
	runGit(t, remote, "commit", "-q", "-m", "mixed content")

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	commit, err := c.ResolveRef(context.Background(), repoPath, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	dir, cleanup, err := c.MaterializeWorktree(context.Background(), repoPath, commit, []string{"*.md"})
	if err != nil {
		t.Fatalf("MaterializeWorktree: %v", err)
	}
	defer cleanup()

	for _, want := range []string{"README.md", "docs/guide.md"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected %s to be checked out: %v", want, err)
		}
	}
	for _, notWant := range []string{"main.go", "data.bin"} {
		if _, err := os.Stat(filepath.Join(dir, notWant)); !os.IsNotExist(err) {
			t.Errorf("expected %s to be excluded by sparse-checkout (err=%v)", notWant, err)
		}
	}
}

// TestMaterializeWorktreeFallsBackToFullCheckoutWhenPatternsMatchNothing
// covers the fallback path: a pattern set matching nothing in this
// tree (a repo whose docs live somewhere the default patterns don't
// cover) still produces a usable, fully-populated worktree rather than
// silently indexing nothing for the dependency.
func TestMaterializeWorktreeFallsBackToFullCheckoutWhenPatternsMatchNothing(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t) // README.md
	writeFileT(t, remote, "main.go", "package main\n")
	runGit(t, remote, "add", ".")
	runGit(t, remote, "commit", "-q", "-m", "go only")

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	commit, err := c.ResolveRef(context.Background(), repoPath, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}

	// *.rs matches nothing at all in this tree (not even README.md).
	dir, cleanup, err := c.MaterializeWorktree(context.Background(), repoPath, commit, []string{"*.rs"})
	if err != nil {
		t.Fatalf("MaterializeWorktree: %v", err)
	}
	defer cleanup()

	for _, want := range []string{"README.md", "main.go"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("expected fallback full checkout to include %s: %v", want, err)
		}
	}
}

// TestEnsureMirrorAndSparseCheckoutSkipsNonMatchingBlobBytes is GIT-005's
// quantitative acceptance criterion: a real before/after measurement,
// not just "fewer files checked out." EnsureMirror must use file:// (not
// a plain local path) — git silently ignores --filter for local-path
// clones ("--filter is ignored in local clones; use file:// instead."),
// verified directly while building this ticket; only file://s and real
// remote URLs (http/ssh) actually exercise the partial-clone protocol.
func TestEnsureMirrorAndSparseCheckoutSkipsNonMatchingBlobBytes(t *testing.T) {
	requireGit(t)
	remoteDir := filepath.Join(t.TempDir(), "remote")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir remote: %v", err)
	}
	runGit(t, remoteDir, "init", "-q", "-b", "main")
	runGit(t, remoteDir, "config", "uploadpack.allowFilter", "true")
	writeFileT(t, remoteDir, "README.md", "hello\n")

	// A large non-doc blob standing in for vendored/binary content a
	// real repo might carry — the whole point of this test is proving
	// its bytes are never fetched into the mirror at all.
	large := make([]byte, 2*1024*1024)
	rand.New(rand.NewSource(1)).Read(large)
	if err := os.WriteFile(filepath.Join(remoteDir, "big.bin"), large, 0o644); err != nil {
		t.Fatalf("write big.bin: %v", err)
	}
	runGit(t, remoteDir, "add", ".")
	runGit(t, remoteDir, "commit", "-q", "-m", "mixed content")

	c := NewCache(t.TempDir())
	repoPath, err := c.EnsureMirror(context.Background(), "file://"+remoteDir)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	sizeAfterMirror := dirSize(t, repoPath)

	commit, err := c.ResolveRef(context.Background(), repoPath, "HEAD")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	worktreeDir, cleanup, err := c.MaterializeWorktree(context.Background(), repoPath, commit, []string{"*.md"})
	if err != nil {
		t.Fatalf("MaterializeWorktree: %v", err)
	}
	defer cleanup()
	sizeAfterCheckout := dirSize(t, repoPath)

	t.Logf("GIT-005 measured: mirror size after blobless clone = %d bytes; after sparse checkout of *.md = %d bytes; big.bin alone = %d bytes", sizeAfterMirror, sizeAfterCheckout, len(large))

	if grew := sizeAfterCheckout - sizeAfterMirror; grew > 100_000 {
		t.Errorf("mirror grew by %d bytes after a *.md-only checkout — the 2MB big.bin blob was likely fetched despite not matching the sparse pattern", grew)
	}
	if _, err := os.Stat(filepath.Join(worktreeDir, "README.md")); err != nil {
		t.Errorf("README.md missing from sparse checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(worktreeDir, "big.bin")); !os.IsNotExist(err) {
		t.Errorf("big.bin should be excluded by sparse-checkout (err=%v)", err)
	}
}

func dirSize(t *testing.T, dir string) int64 {
	t.Helper()
	var total int64
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return total
}
