package git

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"aleutian-ai/ragctl/internal/executil"
)

// ListFiles returns every repo-relative path at commit whose base name is
// name (e.g. "package.json"), read from the bare mirror's tree object
// directly — no worktree checkout needed.
func (c *Cache) ListFiles(ctx context.Context, repoPath, commit, name string) ([]string, error) {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "ls-tree", "-r", "--name-only", commit},
		Timeout: defaultLocalTimeout,
	})
	if err != nil {
		return nil, &CacheError{Op: "ListFiles", Kind: ErrKindTransient, Cause: err}
	}
	if result.ExitCode != 0 {
		return nil, &CacheError{
			Op:    "ListFiles",
			Kind:  ErrKindPermanent,
			Cause: fmt.Errorf("git ls-tree %s: %s", commit, bytes.TrimSpace(result.Stderr)),
		}
	}
	var matches []string
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		if line == "" {
			continue
		}
		if idx := strings.LastIndexByte(line, '/'); idx >= 0 {
			if line[idx+1:] == name {
				matches = append(matches, line)
			}
		} else if line == name {
			matches = append(matches, line)
		}
	}
	return matches, nil
}

// ReadFile returns the content of path as it exists at commit, read
// directly from the bare mirror — no worktree checkout needed.
func (c *Cache) ReadFile(ctx context.Context, repoPath, commit, path string) ([]byte, error) {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "show", commit + ":" + path},
		Timeout: defaultLocalTimeout,
	})
	if err != nil {
		return nil, &CacheError{Op: "ReadFile", Kind: ErrKindTransient, Cause: err}
	}
	if result.ExitCode != 0 {
		return nil, &CacheError{
			Op:    "ReadFile",
			Kind:  ErrKindPermanent,
			Cause: fmt.Errorf("git show %s:%s: %s", commit, path, bytes.TrimSpace(result.Stderr)),
		}
	}
	return result.Stdout, nil
}
