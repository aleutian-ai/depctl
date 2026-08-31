package git

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"aleutian-ai/ragctl/internal/executil"
)

// MaterializeWorktree checks out commit from repoPath into a fresh
// temporary directory via `git worktree add --detach`, for normalization
// to read. Callers must call the returned cleanup func — typically via
// defer — even if the caller's own context is later cancelled; cleanup
// runs on a background context specifically so cancellation of the
// caller's context can't leave a dangling worktree.
func (c *Cache) MaterializeWorktree(ctx context.Context, repoPath, commit string) (string, func() error, error) {
	worktreeDir, err := os.MkdirTemp("", "ragctl-worktree-*")
	if err != nil {
		return "", nil, &CacheError{Op: "MaterializeWorktree", Kind: ErrKindPermanent, Cause: err}
	}

	result, runErr := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "worktree", "add", "--detach", worktreeDir, commit},
		Timeout: defaultLocalTimeout,
	})
	if runErr != nil || result.ExitCode != 0 {
		os.RemoveAll(worktreeDir)
		cause := runErr
		if cause == nil {
			cause = fmt.Errorf("git worktree add %s %s: %s", worktreeDir, commit, bytes.TrimSpace(result.Stderr))
		}
		return "", nil, &CacheError{Op: "MaterializeWorktree", Kind: ErrKindPermanent, Cause: cause}
	}

	cleanup := func() error {
		bg := context.Background()
		result, err := executil.Run(bg, executil.RunOptions{
			Dir:     repoPath,
			Args:    []string{"git", "worktree", "remove", "--force", worktreeDir},
			Timeout: defaultLocalTimeout,
		})
		if err == nil && result.ExitCode == 0 {
			return nil
		}

		// The worktree registration is gone or broken (e.g. directory
		// removed externally) — best-effort clean up what's left rather
		// than crash the pipeline over cleanup failure.
		os.RemoveAll(worktreeDir)
		_, _ = executil.Run(bg, executil.RunOptions{
			Dir:     repoPath,
			Args:    []string{"git", "worktree", "prune"},
			Timeout: defaultLocalTimeout,
		})
		if err != nil {
			return fmt.Errorf("git worktree cleanup for %s: %w", worktreeDir, err)
		}
		return fmt.Errorf("git worktree cleanup for %s: remove exited %d: %s", worktreeDir, result.ExitCode, bytes.TrimSpace(result.Stderr))
	}

	return worktreeDir, cleanup, nil
}
