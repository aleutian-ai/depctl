# GIT-002: Version checkout abstraction

**Epic:** Git Acquisition
**Status:** planned
**Depends on:** GIT-001
**Estimated size:** medium

## Goal
Materialize the file tree of a resolved commit into a temporary location for normalization, without copying the full repository history per version.

## Non-goals
- Does not decide which files matter for documentation (NORM-* handles that after materialization).
- Does not implement content-addressable extraction beyond what `git worktree`/`git archive` already provide.

## Simplicity constraints
- Use `git worktree add --detach <tmpdir> <commit>` against the bare mirror from GIT-001, then `git worktree remove` (or `rm -rf` + `git worktree prune`) after normalization completes. Do not build a custom tree-extraction mechanism — `git worktree` is sufficient and simplest for v0.1.
- One worktree at a time per repository is fine for v0.1; do not build a worktree pool.

## Design
- Package: `internal/source/git`
- ```go
  func (c *Cache) MaterializeWorktree(ctx context.Context, repoPath, commit string) (worktreeDir string, cleanup func() error, err error)
  ```
- `worktreeDir` is created under a scratch directory (e.g. `os.MkdirTemp`), and `cleanup()` removes the worktree registration and directory. Callers **must** defer `cleanup()`.
- Cleanup must run even on context cancellation — use a `defer` immediately after successful creation in the caller, and ensure `MaterializeWorktree` itself doesn't leave partial state if it fails partway (best-effort cleanup on its own error path too).

## Inputs / Outputs
- Input: bare repo path (from GIT-001), resolved commit SHA.
- Output: temp directory containing the checked-out tree; a cleanup function.

## Failure behavior
- Worktree creation failure (e.g. commit not found, disk full): typed error, no dangling temp directory left behind.
- If cleanup fails (e.g. directory removed externally), log a warning — don't crash the pipeline over cleanup failure.

## Tests
- Two versions materialized sequentially into separate temp dirs, both succeed, content differs as expected.
- Cleanup occurs after normal completion (temp dir gone, `git worktree list` no longer shows it).
- Cleanup occurs after simulated cancellation (context cancelled mid-normalization) — verified via a test that cancels context and checks no leaked worktree/dir remains.

## Acceptance criteria
- [x] Two versions can be materialized sequentially without leftover state from the first.
- [x] Cleanup occurs reliably after cancellation, not just on the happy path.
