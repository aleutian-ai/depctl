// Package git implements ragctl's local Git acquisition layer: one shared
// bare mirror per repository, on-demand tag fetches, ref-to-commit
// resolution, disposable worktree materialization, and file-level delta
// computation between two commits. Every operation shells out to the
// system git binary via internal/executil — no Git wire protocol or object
// parsing is implemented directly.
package git

import (
	"bytes"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
}

// NewCache returns a Cache rooted at dir, e.g. "<data-dir>/git".
func NewCache(dir string) *Cache {
	return &Cache{root: dir}
}

// EnsureMirror clones rawURL as a bare mirror under the cache root if one
// doesn't already exist locally; an existing mirror is left untouched —
// call FetchTags to update it.
func (c *Cache) EnsureMirror(ctx context.Context, rawURL string) (string, error) {
	repoPath, err := mirrorPath(c.root, rawURL)
	if err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindPermanent, Cause: err}
	}

	if _, statErr := os.Stat(repoPath); statErr == nil {
		return repoPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindPermanent, Cause: err}
	}

	result, err := executil.Run(ctx, executil.RunOptions{
		Args:    []string{"git", "clone", "--mirror", rawURL, repoPath},
		Timeout: defaultCloneTimeout,
	})
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
