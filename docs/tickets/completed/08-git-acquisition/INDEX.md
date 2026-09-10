# Epic: Git Acquisition

Acquires versioned source content via Git, preferring source over scraped HTML wherever possible. Maintains one shared bare mirror per repository, materializes specific commits into disposable worktrees for normalization, and computes file-level deltas as a normalization optimization hint (content hashing remains the authoritative change signal).

## Tickets
- [GIT-001](GIT-001-git-cache-manager.md) — Bare-mirror cache: ensure mirror, fetch tags, resolve version → commit.
- [GIT-002](GIT-002-version-checkout-abstraction.md) — Materialize a commit into a temporary worktree, with reliable cleanup.
- [GIT-003](GIT-003-source-delta-calculation.md) — Compute added/modified/deleted/renamed file deltas between two commits.
