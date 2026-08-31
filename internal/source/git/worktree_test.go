package git

import (
	"context"
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

	dir1, cleanup1, err := c.MaterializeWorktree(context.Background(), repoPath, v1)
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

	dir2, cleanup2, err := c.MaterializeWorktree(context.Background(), repoPath, v2)
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
	dir, cleanup, err := c.MaterializeWorktree(ctx, repoPath, v1)
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
