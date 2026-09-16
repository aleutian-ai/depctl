// Package git implements ragctl's local Git acquisition layer: one shared
// bare mirror per repository (cloned blobless via --filter=blob:none,
// GIT-005 — full history/tags transfer, file content is fetched lazily
// on checkout), on-demand tag fetches, ref-to-commit resolution,
// sparse-checkout-scoped disposable worktree materialization (only the
// doc-shaped files a normalizer would actually read, with a transparent
// fallback to a full checkout if the pattern set matches nothing), and
// file-level delta computation between two commits. Every operation
// shells out to the system git binary via internal/executil — no Git
// wire protocol or object parsing is implemented directly.
package git

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"context"

	"aleutian-ai/ragctl/internal/executil"
)

// defaultCloneTimeout bounds git clone/fetch, which can be slow over the
// network for large repositories.
const defaultCloneTimeout = 10 * time.Minute

// defaultLocalTimeout bounds git operations that never touch the network
// (rev-parse, worktree add/remove, diff).
const defaultLocalTimeout = 30 * time.Second

// Cache manages a local bare-mirror cache of Git repositories, one mirror
// per repository URL shared across every dependency version of that
// package.
type Cache struct {
	root string

	mirrorLocksMu sync.Mutex
	mirrorLocks   map[string]*sync.Mutex
}

// NewCache returns a Cache rooted at dir, e.g. "<data-dir>/git".
func NewCache(dir string) *Cache {
	return &Cache{root: dir, mirrorLocks: map[string]*sync.Mutex{}}
}

// lockMirror serializes every EnsureMirror call for the same repoPath,
// so two concurrent first-time acquisitions of the same repository (a
// real possibility once a planner drives multiple dependency builds
// concurrently) can't both run `git clone --mirror` into the same
// target directory at once — a shared resource across every generation
// of that dependency, not scoped to one build, so a corrupted mirror
// here would silently break every future build of that dependency, not
// just the racing ones. Returns an unlock func to defer.
func (c *Cache) lockMirror(repoPath string) func() {
	c.mirrorLocksMu.Lock()
	l, ok := c.mirrorLocks[repoPath]
	if !ok {
		l = &sync.Mutex{}
		c.mirrorLocks[repoPath] = l
	}
	c.mirrorLocksMu.Unlock()

	l.Lock()
	return l.Unlock
}

// EnsureMirror clones rawURL as a bare mirror under the cache root if one
// doesn't already exist locally; an existing mirror is left untouched —
// call FetchTags to update it.
//
// The clone uses --filter=blob:none (a blobless partial clone, GIT-005):
// full commit/tag history and tree metadata are still transferred in
// full — cross-version diffing needs that — but file content (blobs)
// is fetched lazily, only when something actually checks it out.
// Real hosts that don't support the filter (or a local git old enough
// not to know the flag at all) don't need special-casing here: a
// server lacking filter support just warns and clones unfiltered
// automatically (verified directly against a real git invocation while
// building this ticket) — only a local git literally too old to parse
// the flag fails outright, handled below with one plain retry.
func (c *Cache) EnsureMirror(ctx context.Context, rawURL string) (string, error) {
	repoPath, err := mirrorPath(c.root, rawURL)
	if err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindPermanent, Cause: err}
	}
	defer c.lockMirror(repoPath)()

	if _, statErr := os.Stat(repoPath); statErr == nil {
		return repoPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindPermanent, Cause: err}
	}

	result, err := executil.Run(ctx, executil.RunOptions{
		Args:    []string{"git", "clone", "--mirror", "--filter=blob:none", rawURL, repoPath},
		Timeout: defaultCloneTimeout,
	})
	if err != nil || result.ExitCode != 0 {
		// A local git too old to know --filter at all is the only
		// realistic case that reaches here rather than degrading
		// gracefully on its own — retry once without it.
		os.RemoveAll(repoPath)
		result, err = executil.Run(ctx, executil.RunOptions{
			Args:    []string{"git", "clone", "--mirror", rawURL, repoPath},
			Timeout: defaultCloneTimeout,
		})
	}
	if err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindTransient, Cause: err}
	}
	if result.ExitCode != 0 {
		os.RemoveAll(repoPath)
		return "", &CacheError{
			Op:    "EnsureMirror",
			Kind:  ErrKindTransient,
			Cause: fmt.Errorf("git clone --mirror %s: %s", rawURL, bytes.TrimSpace(result.Stderr)),
		}
	}
	return repoPath, nil
}

// FetchTags fetches new tags/refs into an existing bare mirror at
// repoPath.
func (c *Cache) FetchTags(ctx context.Context, repoPath string) error {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "fetch", "--tags"},
		Timeout: defaultCloneTimeout,
	})
	if err != nil {
		return &CacheError{Op: "FetchTags", Kind: ErrKindTransient, Cause: err}
	}
	if result.ExitCode != 0 {
		return &CacheError{
			Op:    "FetchTags",
			Kind:  ErrKindTransient,
			Cause: fmt.Errorf("git fetch --tags: %s", bytes.TrimSpace(result.Stderr)),
		}
	}
	return nil
}

// ResolveRef resolves ref (a tag, branch, or commit-ish) to a commit SHA
// purely from local cache — no network access.
func (c *Cache) ResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "rev-parse", ref + "^{commit}"},
		Timeout: defaultLocalTimeout,
	})
	if err != nil {
		return "", &CacheError{Op: "ResolveRef", Kind: ErrKindPermanent, Cause: err}
	}
	if result.ExitCode != 0 {
		return "", &CacheError{
			Op:    "ResolveRef",
			Kind:  ErrKindPermanent,
			Cause: fmt.Errorf("git rev-parse %s: %s", ref, bytes.TrimSpace(result.Stderr)),
		}
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

// mirrorPath computes the cache-relative path for rawURL, following the
// "<host>/<org>/<repo>.git" convention (e.g. github.com/grpc/grpc-go.git).
func mirrorPath(root, rawURL string) (string, error) {
	host, path, err := splitGitURL(rawURL)
	if err != nil {
		return "", err
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	if path == "" {
		return "", fmt.Errorf("git: URL %q has no repository path", rawURL)
	}

	segments := strings.Split(path, "/")
	segments[len(segments)-1] += ".git"
	return filepath.Join(append([]string{root, host}, segments...)...), nil
}

// splitGitURL splits a repository URL into a host and a repository path.
// It accepts standard URLs (https://host/org/repo.git), SCP-like syntax
// (git@host:org/repo.git), and plain local filesystem paths (used by
// tests, and by anyone mirroring from a local path) — the latter are
// rooted under a fixed "local" host so the cache layout stays consistent.
func splitGitURL(rawURL string) (host, path string, err error) {
	if u, uerr := url.Parse(rawURL); uerr == nil && u.Scheme != "" && u.Host != "" {
		return u.Host, u.Path, nil
	}
	if idx := strings.Index(rawURL, "@"); idx >= 0 {
		rest := rawURL[idx+1:]
		if colon := strings.Index(rest, ":"); colon >= 0 {
			return rest[:colon], rest[colon+1:], nil
		}
	}

	abs, aerr := filepath.Abs(rawURL)
	if aerr != nil {
		return "", "", fmt.Errorf("git: cannot parse repository URL %q", rawURL)
	}
	return "local", filepath.ToSlash(abs), nil
}
