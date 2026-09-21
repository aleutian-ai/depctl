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
//
// When sparsePatterns is non-empty (GIT-005), only files matching those
// patterns are actually fetched/checked out — the point of pairing this
// with EnsureMirror's blobless clone: a doc-shaped subset of files,
// without ever fetching the blobs for everything else. If the patterns
// match nothing at all in this commit's tree (a repo whose docs live
// somewhere the default set doesn't cover), MaterializeWorktree falls
// back to a full checkout transparently — callers never see the
// difference, and normalization never silently sees an empty worktree
// because of the sparse pattern set alone.
func (c *Cache) MaterializeWorktree(ctx context.Context, repoPath, commit string, sparsePatterns []string) (string, func() error, error) {
	worktreeDir, err := os.MkdirTemp("", "ragctl-worktree-*")
	if err != nil {
		return "", nil, &CacheError{Op: "MaterializeWorktree", Kind: ErrKindPermanent, Cause: err}
	}

	addArgs := []string{"git", "worktree", "add", "--detach"}
	if len(sparsePatterns) > 0 {
		// Populated explicitly below, once sparse-checkout is configured
		// — otherwise `add` would do a full checkout first and defeat the
		// point of pairing this with a blobless mirror.
		addArgs = append(addArgs, "--no-checkout")
	}
	addArgs = append(addArgs, worktreeDir, commit)

	unlockAdd := c.lockMirror(repoPath)
	result, runErr := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    addArgs,
		Timeout: defaultLocalTimeout,
	})
	unlockAdd()
	if runErr != nil || result.ExitCode != 0 {
		os.RemoveAll(worktreeDir)
		cause := runErr
		if cause == nil {
			cause = fmt.Errorf("git worktree add %s %s: %s", worktreeDir, commit, bytes.TrimSpace(result.Stderr))
		}
		return "", nil, &CacheError{Op: "MaterializeWorktree", Kind: ErrKindPermanent, Cause: cause}
	}

	cleanup := worktreeCleanup(repoPath, worktreeDir)

	if len(sparsePatterns) > 0 {
		if err := c.checkoutSparseOrFallback(ctx, repoPath, worktreeDir, commit, sparsePatterns); err != nil {
			cleanup()
			return "", nil, &CacheError{Op: "MaterializeWorktree", Kind: ErrKindPermanent, Cause: err}
		}
	}

	return worktreeDir, cleanup, nil
}

// checkoutSparseOrFallback configures non-cone sparse-checkout in
// worktreeDir (worktree add --no-checkout leaves it empty beforehand)
// and checks out commit. Non-cone, not --cone: cone mode only accepts
// whole-directory patterns and rejects extension globs like "*.md"
// outright ("specify directories rather than patterns") — verified
// directly against a real git invocation while building this ticket.
// If the resulting checkout is empty (the patterns matched nothing in
// this tree), sparse-checkout is disabled and a full checkout is done
// instead.
func (c *Cache) checkoutSparseOrFallback(ctx context.Context, repoPath, worktreeDir, commit string, patterns []string) error {
	// init/set/disable write the mirror's shared config file, which git
	// guards with a lock file — concurrent worktrees of one mirror
	// (monorepo submodules synced in parallel) otherwise fail with "could
	// not lock config file". The slow checkout itself stays unlocked.
	if err := c.configureSparse(ctx, repoPath, worktreeDir, patterns); err != nil {
		return err
	}
	return c.finishSparseCheckout(ctx, repoPath, worktreeDir, commit)
}

func (c *Cache) configureSparse(ctx context.Context, repoPath, worktreeDir string, patterns []string) error {
	defer c.lockMirror(repoPath)()
	if res, runErr := executil.Run(ctx, executil.RunOptions{
		Dir: worktreeDir, Args: []string{"git", "sparse-checkout", "init", "--no-cone"}, Timeout: defaultLocalTimeout,
	}); runErr != nil || res.ExitCode != 0 {
		return gitStepErr("sparse-checkout init", runErr, res)
	}

	setArgs := append([]string{"git", "sparse-checkout", "set"}, patterns...)
	if res, runErr := executil.Run(ctx, executil.RunOptions{
		Dir: worktreeDir, Args: setArgs, Timeout: defaultLocalTimeout,
	}); runErr != nil || res.ExitCode != 0 {
		return gitStepErr("sparse-checkout set", runErr, res)
	}

	return nil
}

func (c *Cache) finishSparseCheckout(ctx context.Context, repoPath, worktreeDir, commit string) error {
	if err := checkoutCommit(ctx, worktreeDir, commit); err != nil {
		return err
	}

	entries, err := os.ReadDir(worktreeDir)
	if err != nil {
		return fmt.Errorf("read worktree %s: %w", worktreeDir, err)
	}
	empty := true
	for _, e := range entries {
		if e.Name() != ".git" {
			empty = false
			break
		}
	}
	if !empty {
		return nil
	}

	// The sparse pattern set matched nothing in this commit's tree —
	// disable it and check out everything instead, rather than silently
	// indexing nothing for this dependency.
	unlock := c.lockMirror(repoPath)
	res, runErr := executil.Run(ctx, executil.RunOptions{
		Dir: worktreeDir, Args: []string{"git", "sparse-checkout", "disable"}, Timeout: defaultLocalTimeout,
	})
	unlock()
	if runErr != nil || res.ExitCode != 0 {
		return gitStepErr("sparse-checkout disable", runErr, res)
	}
	return checkoutCommit(ctx, worktreeDir, commit)
}

func checkoutCommit(ctx context.Context, worktreeDir, commit string) error {
	res, runErr := executil.Run(ctx, executil.RunOptions{
		Dir: worktreeDir, Args: []string{"git", "checkout", commit}, Timeout: defaultLocalTimeout,
	})
	if runErr != nil || res.ExitCode != 0 {
		return gitStepErr("checkout "+commit, runErr, res)
	}
	return nil
}

func gitStepErr(step string, runErr error, res executil.RunResult) error {
	if runErr != nil {
		return fmt.Errorf("git %s: %w", step, runErr)
	}
	return fmt.Errorf("git %s: exit %d: %s", step, res.ExitCode, bytes.TrimSpace(res.Stderr))
}

func worktreeCleanup(repoPath, worktreeDir string) func() error {
	return func() error {
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
}
