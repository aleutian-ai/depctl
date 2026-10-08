package git

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/aleutian-ai/depctl/internal/httplimit"
)

// SEC-003: fetch limits — a real repository's mirror clone is rejected
// once it exceeds the configured size cap.

func TestEnsureMirrorRejectsMirrorOverTheSizeLimit(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	// newRemoteFixture already committed real content; tag it so there's
	// at least one real ref to clone.
	runGit(t, remote, "tag", "v1.0.0")

	cacheRoot := t.TempDir()
	c := NewCache(cacheRoot, WithMaxMirrorBytes(1)) // deliberately tiny

	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err == nil {
		t.Fatalf("EnsureMirror succeeded with a 1-byte cap, want ErrFetchLimitExceeded (repoPath=%s)", repoPath)
	}
	if !errors.Is(err, httplimit.ErrFetchLimitExceeded) {
		t.Errorf("error = %v, want it to wrap httplimit.ErrFetchLimitExceeded", err)
	}
	var cacheErr *CacheError
	if errors.As(err, &cacheErr) && cacheErr.Kind != ErrKindPermanent {
		t.Errorf("CacheError.Kind = %v, want ErrKindPermanent (a repository this size will never fit, retrying won't help)", cacheErr.Kind)
	}
}

func TestEnsureMirrorSucceedsUnderTheSizeLimit(t *testing.T) {
	requireGit(t)

	remote := newRemoteFixture(t)
	runGit(t, remote, "tag", "v1.0.0")

	cacheRoot := t.TempDir()
	c := NewCache(cacheRoot, WithMaxMirrorBytes(100*1024*1024)) // generous — this fixture is tiny

	repoPath, err := c.EnsureMirror(context.Background(), remote)
	if err != nil {
		t.Fatalf("EnsureMirror: %v", err)
	}
	if _, err := os.Stat(repoPath); err != nil {
		t.Errorf("mirror not present at %s: %v", repoPath, err)
	}
}

func TestMirrorDirSizeSumsRealFileBytes(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a", []byte("12345"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.MkdirAll(dir+"/sub", 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	if err := os.WriteFile(dir+"/sub/b", []byte("1234567890"), 0o644); err != nil {
		t.Fatalf("write sub/b: %v", err)
	}
	size, err := mirrorDirSize(dir)
	if err != nil {
		t.Fatalf("mirrorDirSize: %v", err)
	}
	if size != 15 {
		t.Errorf("mirrorDirSize = %d, want 15 (5 + 10 bytes, directories excluded)", size)
	}
}
