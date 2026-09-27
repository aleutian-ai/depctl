package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// newCheckoutFixture creates a real working-tree checkout of remote
// under dir/relPath (e.g. "go/terraform", matching offline-knowledge's
// own <category>/<reponame> shape) — a real `git clone`, not a bare
// mirror, so the checkout has its own real .git directory. Its origin
// remote is then rewritten to originURL: decoupling "what it was
// actually cloned from" (a real, reachable local fixture, so the clone
// itself succeeds) from "what identity findCheckoutSeed will match
// against" (a deliberately unreachable URL, so a passing test proves
// the seed was used, not that the URL happened to be locally clonable).
func newCheckoutFixture(t *testing.T, root, relPath, remote, originURL string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	runGit(t, root, "clone", "-q", remote, dir)
	runGit(t, dir, "remote", "set-url", "origin", originURL)
	return dir
}

const fakeUpstreamURL = "https://invalid.invalid/upstream/repo.git"

func TestEnsureMirrorSeedsFromCheckoutTwoLevelsUnderRoot(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	searchRoot := t.TempDir()
	newCheckoutFixture(t, searchRoot, "go/terraform", remote, fakeUpstreamURL)

	c := NewCache(t.TempDir(), WithCheckoutSearchRoots([]string{searchRoot}))
	repoPath, err := c.EnsureMirror(context.Background(), fakeUpstreamURL)
	if err != nil {
		t.Fatalf("EnsureMirror: %v (must seed from the discovered checkout, never touch the network)", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0"); err != nil {
		t.Errorf("seeded mirror doesn't resolve the fixture's own tag: %v", err)
	}
}

func TestEnsureMirrorSkipsCheckoutWithNonMatchingOrigin(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")
	other := newRemoteFixture(t) // a real, different repo — same shape, different content/identity

	searchRoot := t.TempDir()
	// This checkout's real origin (rewritten below) simply isn't the URL
	// EnsureMirror is looking for.
	newCheckoutFixture(t, searchRoot, "go/other-project", other, "https://invalid.invalid/unrelated/other.git")

	// remote itself (a real local path) stands in for "the network" here
	// — the checkout present doesn't match it, so EnsureMirror must fall
	// all the way through to a real clone of remote, not seed from the
	// non-matching checkout.
	c := NewCache(t.TempDir(), WithCheckoutSearchRoots([]string{searchRoot}))
	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if _, err := c.ResolveRef(context.Background(), repoPath, "v1.0.0"); err != nil {
		t.Errorf("expected the real clone of remote, not the non-matching checkout: %v", err)
	}
}

// TestFindCheckoutSeedNeverDescendsIntoADiscoveredCheckout is the real,
// distinguishing regression test for the SkipDir pruning: the outer
// checkout's own origin deliberately does NOT match, but a nested repo
// underneath it does. If pruning is correct, the walk stops at the
// outer checkout's .git and never reaches the nested match at all — the
// overall result must be a miss. If the SkipDir return were ever
// removed, the walk would keep descending, reach the nested repo, and
// wrongly report a hit.
func TestFindCheckoutSeedNeverDescendsIntoADiscoveredCheckout(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t)
	nestedRemote := newRemoteFixture(t)

	searchRoot := t.TempDir()
	outer := newCheckoutFixture(t, searchRoot, "go/terraform", remote, "https://invalid.invalid/unrelated/outer.git")
	newCheckoutFixture(t, outer, "vendor/nested-repo", nestedRemote, fakeUpstreamURL)

	c := NewCache(t.TempDir(), WithCheckoutSearchRoots([]string{searchRoot}))
	if _, ok := c.findCheckoutSeed(context.Background(), fakeUpstreamURL); ok {
		t.Error("findCheckoutSeed: ok = true — walked past the outer checkout's own .git and found a nested match, want it to stop at the outer .git and miss")
	}
}

func TestEnsureMirrorMultipleRootsFirstMatchWins(t *testing.T) {
	requireGit(t)
	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	rootA, rootB := t.TempDir(), t.TempDir()
	newCheckoutFixture(t, rootA, "go/widget", remote, fakeUpstreamURL)
	newCheckoutFixture(t, rootB, "go/widget", remote, fakeUpstreamURL) // same identity — rootA must win by order

	c := NewCache(t.TempDir(), WithCheckoutSearchRoots([]string{rootA, rootB}))
	seed, ok := c.findCheckoutSeed(context.Background(), fakeUpstreamURL)
	if !ok {
		t.Fatal("findCheckoutSeed: ok = false, want true")
	}
	if !isUnderRoot(t, seed, rootA) {
		t.Errorf("seed = %s, want a path under rootA (%s) — the first configured root should win", seed, rootA)
	}
}

func TestFindCheckoutSeedNoConfiguredRootsNeverHits(t *testing.T) {
	c := NewCache(t.TempDir())
	if _, ok := c.findCheckoutSeed(context.Background(), "https://example.com/whatever.git"); ok {
		t.Error("findCheckoutSeed: ok = true with no checkoutSearchRoots configured, want false")
	}
}

func TestCheckoutWithNoOriginRemoteIsSkippedNotFatal(t *testing.T) {
	requireGit(t)

	searchRoot := t.TempDir()
	dir := filepath.Join(searchRoot, "go", "no-origin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	runGit(t, dir, "init", "-q") // a real repo, but never cloned — no "origin" remote at all

	c := NewCache(t.TempDir(), WithCheckoutSearchRoots([]string{searchRoot}))
	if _, ok := c.findCheckoutSeed(context.Background(), fakeUpstreamURL); ok {
		t.Error("findCheckoutSeed: ok = true for a checkout with no origin remote, want false")
	}
}

func isUnderRoot(t *testing.T, path, root string) bool {
	t.Helper()
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !filepath.IsAbs(rel) && rel[0] != '.'
}
