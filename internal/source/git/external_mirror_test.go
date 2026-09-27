package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// unreachableRemote is used as EnsureMirror's rawURL in tests that must
// prove a seed was used without ever touching the network — any real
// clone attempt against it fails fast with a DNS error, so a passing
// test is direct proof the network path was never reached.
const unreachableRemote = "https://invalid.invalid/nonexistent/repo.git"

// seedExternalMirror creates a real bare mirror of remote and places it
// at exactly the path an external root would expect for rawURL —
// mirrorPath's own <host>/<org>/<repo>.git convention.
func seedExternalMirror(t *testing.T, extRoot, rawURL, remote string) {
	t.Helper()
	extPath, err := mirrorPath(extRoot, rawURL)
	if err != nil {
		t.Fatalf("mirrorPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(extPath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	runGit(t, extRoot, "clone", "--mirror", remote, extPath)
}

func TestEnsureMirrorSeedsFromExternalMirrorRoot(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	extRoot := t.TempDir()
	seedExternalMirror(t, extRoot, unreachableRemote, remote)

	c := NewCache(t.TempDir(), WithExternalMirrorRoots([]string{extRoot}))
	repoPath, err := c.EnsureMirror(context.Background(), unreachableRemote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v (must seed from the external root, never touch the network)", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0"); err != nil {
		t.Errorf("seeded mirror doesn't resolve the fixture's own tag: %v", err)
	}
}

func TestEnsureMirrorPrefersFirstMatchingExternalRoot(t *testing.T) {
	requireGit(t)
	remoteA := newRemoteFixture(t)
	runGit(t, remoteA, "tag", "from-root-a")
	remoteB := newRemoteFixture(t)
	runGit(t, remoteB, "tag", "from-root-b")

	rootA, rootB := t.TempDir(), t.TempDir()
	seedExternalMirror(t, rootA, unreachableRemote, remoteA)
	seedExternalMirror(t, rootB, unreachableRemote, remoteB)

	c := NewCache(t.TempDir(), WithExternalMirrorRoots([]string{rootA, rootB}))
	repoPath, err := c.EnsureMirror(context.Background(), unreachableRemote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "from-root-a"); err != nil {
		t.Errorf("seeded mirror missing rootA's tag (rootA, listed first, should have won): %v", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "from-root-b"); err == nil {
		t.Error("seeded mirror has rootB's tag — rootB must never even be checked once rootA matched")
	}
}

func TestEnsureMirrorFallsThroughToNetworkWhenNoExternalRootMatches(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	// A real, existing external root that simply doesn't contain this
	// particular repo — must fall through to the real (here, real local
	// filesystem, standing in for network) clone, not error.
	emptyRoot := t.TempDir()

	c := NewCache(t.TempDir(), WithExternalMirrorRoots([]string{emptyRoot}))
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v, want a clean fall-through to the real clone", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0"); err != nil {
		t.Errorf("ResolveRef after fall-through clone: %v", err)
	}
}

func TestEnsureMirrorExternalRootMissingOnDiskFallsThroughCleanly(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	c := NewCache(t.TempDir(), WithExternalMirrorRoots([]string{filepath.Join(t.TempDir(), "does-not-exist")}))
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v, want a clean fall-through when the configured root doesn't exist at all", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0"); err != nil {
		t.Errorf("ResolveRef after fall-through clone: %v", err)
	}
}
