# Epic: Nested Go Module Normalization Boundary

[STRESS-005](../../planned/49-full-loop-stress-testing/STRESS-005-full-cold-sync-real-scale.md) found that a full cold sync of real `hashicorp/terraform` stalled almost entirely on a single dependency, `cloud.google.com/go`, which alone consumed nearly the entire batch's 30-minute time budget. Investigating why (not just documenting it as a timeout-ceiling problem) found the real root cause: `internal/data/generation/build.go`'s `normalizeSources` walks a checked-out worktree with no awareness of nested Go module boundaries — a subdirectory with its own `go.mod` is a separate, independently-versioned module, not content belonging to the dependency actually being synced.

`cloud.google.com/go`'s real repository is exactly this shape: its root module resolves to a single file (`doc.go`), while the same repository also contains **216 separate `go.mod` files** (`storage/`, `bigquery/`, `pubsub/`, etc., each its own module). Measured directly: the buggy walk touches **13,399** directories with `.go` files; the correctly-scoped walk touches **23** — a ~583x over-normalization, both wildly inflating chunk/embed volume and mislabeling unrelated sibling packages' documentation as if it belonged to `cloud.google.com/go` itself (a correctness bug, not just a performance one).

A second, compounding discovery: fixing the walk alone wasn't enough. GIT-005's sparse-checkout (`internal/normalize/sparse.go`'s `SparsePatterns`) only fetches doc-shaped files for Go (`*.go`, `*.md`, etc.) — not `go.mod` — so the exact signal needed to detect a nested module boundary was being stripped out of the checked-out worktree before normalization ever saw it. Both had to change together.

## Tickets
- [x] [BOUND-001](BOUND-001-skip-nested-go-module-during-normalization.md) — Done: `normalizeSources` now skips any subdirectory with its own `go.mod`; `go.mod` added to the Go sparse-checkout pattern set so the boundary is actually visible to detect. Live-reverified: `cloud.google.com/go` now syncs in ~35s total (clone+scan+sync) instead of consuming an entire 30-minute batch budget and failing.

## Non-goals
- Doesn't change the batch-wide timeout ceiling itself ([epic 51](../51-bulk-sync-batch-timeout/INDEX.md)'s own scope) — this fix removes the dominant real-world cause of hitting it, it doesn't remove the ceiling's existence for some other genuinely huge single dependency.
