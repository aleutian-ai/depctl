# BOUND-001: Skip nested Go module boundaries during normalization

**Epic:** Nested Go Module Normalization Boundary
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
Stop `internal/data/generation/build.go`'s `normalizeSources` from walking past a nested `go.mod` — a subdirectory with its own `go.mod` is a separate, independently-versioned Go module, never content belonging to the dependency currently being synced.

## Design (as implemented)
1. `normalizeSources`'s `filepath.WalkDir` callback: when visiting a directory other than the acquisition root itself, check `hasGoMod(path)` (new helper, `os.Stat` for a `go.mod` file directly in that directory) and return `filepath.SkipDir` if true — the walk never descends into it.
2. `internal/normalize/sparse.go`'s `SparsePatterns(EcosystemGo)` gained `"go.mod"` alongside `"*.go"`. Without this, GIT-005's sparse-checkout strips `go.mod` out of the materialized worktree entirely (it only fetches doc-shaped files), so step 1's check would never see the file it needs — found via a regression test that failed even with the walk-time fix in place, confirmed by inspecting `checkoutSparseOrFallback`'s pattern set directly.

## Tests
- `TestBuildSkipsNestedGoModuleBoundary` (`internal/data/generation/build_test.go`): builds the same fixture repo twice — once as-is, once with an added `sibling/` subdirectory carrying its own `go.mod` and a distinct exported, documented function — and asserts `manifest.ObjectCount` is identical between both. Reproduces the real failure shape (google-cloud-go's root module vs. its 216 sibling modules) with a fast, offline 2-module fixture.
- `TestSparsePatternsIncludesGoModForNestedModuleBoundaryDetection` (`internal/normalize/sparse_test.go`): asserts `go.mod` is in the Go sparse pattern set.
- Full existing suite (`internal/data/generation`, `internal/normalize`, `internal/source/git`) re-run clean on both macOS and Linux (`hack/test-linux.sh`, now with the Ollama/Qdrant host-forwarding setup from the STRESS-005 investigation baked in).

## Live verification
Measured the real repo directly (`github.com/googleapis/google-cloud-go`): the buggy unscoped walk touches **13,399** directories containing `.go` files; correctly scoped to just the root module (excluding all 216 nested `go.mod` boundaries), only **23**.

Re-ran the exact failing case from STRESS-005 — `depctl sync --dependency cloud.google.com/go` against real `hashicorp/terraform` inside the Podman/Linux container — with the fix in place: `OK cloud.google.com/go v0.123.0`, `1 synced, 0 failed, 0 skipped`, and the entire script (fresh clone of terraform + scan + this targeted sync) completed in **35 seconds total**. Before the fix, this single dependency alone consumed the full 30+ minute batch budget in [STRESS-005](../../planned/49-full-loop-stress-testing/STRESS-005-full-cold-sync-real-scale.md)'s original run and still failed with `context deadline exceeded`.

## Acceptance criteria
- [x] A dependency whose repository contains nested Go modules no longer has those sibling modules' content normalized as its own.
- [x] `go.mod` is present in the sparse-checked-out worktree so the boundary is actually detectable.
- [x] Live-reverified against the real dependency (`cloud.google.com/go`) that originally exposed this in STRESS-005.
