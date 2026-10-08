# MONO-001: Decide an approach for Go monorepo/workspace submodule resolution

**Epic:** Go Monorepo/Workspace Resolution
**Status:** done
**Depends on:** none
**Estimated size:** unknown until an option below is picked (ranges small→medium)

## Goal
Pick and scope a real fix for the gap [STRESS-001](../../planned/49-full-loop-stress-testing/STRESS-001-real-dependency-heavy-project-scan.md) found: `depctl scan` fails to resolve any nested submodule of a Go monorepo shaped like `hashicorp/terraform` (10/10 submodules failed with `invalid version: unknown revision 000000000000`), because each submodule's `go.mod` `replace` block only covers *its own* known siblings, and no `go.work` is checked into the repo to fill the rest in.

This ticket is the decision point, not the implementation — write the chosen option up as a real `Design` section (replacing this ticket's own content, or split into a new ticket) once one is picked.

## Relevant existing behavior
`internal/resolver/golang/list.go`'s `listModules` already sets `GOWORK=off` deliberately and explicitly (not an oversight) — the comment there says: *"We detect and resolve per go.mod, not per workspace, so disable workspace mode rather than special-casing it."* That decision predates this finding and was presumably made for a different real case (large single-workspace projects like `kubernetes`, where the repo ships one `go.work` at the root and per-`go.mod` resolution just needs to not be confused by it). Terraform's case is different: it has **no `go.work` at all**, and the submodules only resolve at all via **incomplete local `replace` directives**. Confirmed: even *with* workspace mode enabled, terraform's submodules wouldn't resolve standalone, since there's no `go.work` to enable in the first place — the real gap is the incomplete `replace` coverage, not `GOWORK=off` itself.

## Options

**Option A — Leave as-is, document the limitation.**
Treat this as an accepted, documented gap: depctl resolves each `go.mod` root scan discovers independently; a submodule whose own `go.mod` can't self-resolve (because it depends on local-only placeholder versions its own `replace` block doesn't fully cover) surfaces a clear `resolve error`, is skipped, and the rest of the scan proceeds normally (already true today — the other resolvable submodules/root aren't blocked by one failing). Lowest cost, but means depctl silently under-covers real dependencies for any project shaped like terraform's plugin submodules — an agent asking about `internal/backend/remote-state/azure`'s dependencies gets nothing.

**Option B — Synthesize a temporary `go.work` per scan, scoped to the repo.**
When `depctl scan` discovers multiple `go.mod` roots under one repository root, generate an in-memory/temp-file `go.work` listing all of them (`go work init` + `go work use` for each discovered root, written to a scratch dir), and pass `GOWORK=<temp path>` instead of `GOWORK=off` specifically for resolving submodules that fail standalone. This directly targets the observed failure (Go's own module resolver, given a workspace covering all the sibling directories, can resolve the placeholder pseudo-versions via the workspace's local module set, same mechanism `replace` uses but complete instead of partial). Needs care: only apply this as a *fallback* after a standalone resolve fails (avoid changing behavior for the common single-module or complete-`go.work` case), and needs a real test against a small hand-built two-sibling-module fixture, not just terraform.

**Option C — Detect and report the failure mode distinctly, without fixing resolution.**
Have `list.go` recognize the specific `invalid version: unknown revision 000000000000` pattern and surface a distinct, clearer error/warning (e.g. "submodule requires workspace-mode resolution (go.work), not supported standalone") instead of today's generic `go list exited 1` wrapped error — better diagnostics for Option A's limitation, without attempting Option B's fix. Could be a fast, small first step regardless of whether B is ever built.

## Recommendation
~~Start with **C**...~~ Superseded: B is the only option that produces a correct answer rather than a clearer wrong one, its scope turned out to be small and well-bounded (no nested-workspace or cross-repo edge cases to handle for the observed shape), so it was built directly instead of staging through C first. See Post-implementation note.

## Non-goals (for whichever option is picked)
- No support for a `go.work` that references modules *outside* the scanned project root (e.g. a sibling repo checked out elsewhere) — out of scope regardless of option.
- No change to `GOWORK=off`'s existing behavior for the common case (a single `go.mod`, or a project with a real committed `go.work` at its root) — whichever option ships must be additive/fallback-only, not a regression risk for what already works.

## Tests
- Whichever option is picked needs a small, fast, hand-built fixture (two sibling `go.mod`s with local `replace` directives to each other, deliberately missing one sibling from one of the two `replace` blocks — reproduces the terraform failure shape without needing a real clone) in addition to a live re-run against terraform.

## Acceptance criteria
- [x] An option (A/B/C, or a combination) is chosen and written up as a real, buildable `Design` section.
- [x] If B is chosen: `depctl scan` against `hashicorp/terraform` resolves all 11 discovered Go projects (root + 10 submodules), not just the root.

## Design (as implemented — Option B)

`internal/resolver/golang/list.go`'s `listModules` now tries the existing fast path first (`GOWORK=off`, standalone per-`go.mod` resolution — unchanged for every project that isn't shaped like this). Only when that fails with `isUnresolvedLocalReplaceError` (the placeholder pseudo-version `v0.0.0-00010101000000-000000000000`, either "invalid version: unknown revision" against a real proxy or "module lookup disabled" under `GOPROXY=off`) does it fall back:

1. `internal/resolver/golang/workspace_fallback.go`'s `synthesizeWorkspace` walks up from the failing root to the nearest `.git` (repo root), then walks the whole repo collecting every directory with a `go.mod` (skipping `.git`/`vendor`/`node_modules`).
2. If fewer than 2 are found, the fallback isn't applicable (bails out, original error returned — a workspace of one module can't help).
3. Writes a temp `go.work` (`go env GOVERSION` for the directive, `use` listing every discovered root as an absolute quoted path) to a scratch temp dir.
4. Re-runs `go list -m -json all` in the original root with `GOWORK=<temp path>` (and `GOFLAGS=` cleared, since `-mod=mod` is invalid in workspace mode) instead of `GOWORK=off`.
5. On success, uses those results; on any fallback failure (no `.git` found, <2 go.mod roots, or the workspace retry itself fails), returns the *original* standalone error, not the fallback's — the standalone error is the more informative one when the fallback can't help.
6. Cleans up the temp dir unconditionally.

Live-reverified against `hashicorp/terraform`: all 11 discovered projects (root + `internal/backend/remote-state/{azure,consul,cos,gcs,kubernetes,oci,oss,pg,s3}` + `internal/legacy`) now resolve — root at 569 deps, each submodule at 561 (root's dependency set minus the local sibling-only requires). Regression test `TestListModulesWorkspaceFallbackForIncompleteSiblingReplace` in `internal/resolver/golang/list_test.go` reproduces the exact failure shape with a 3-module hand-built fixture (main + sub1 + sub2, sub1 requiring sub2 at the zero-time placeholder version with no local replace of its own), fast and offline. All existing `list_test.go` tests (vendor-mode, workspace-mode, toolchain-download avoidance, context cancellation) still pass unchanged, confirming the fallback is additive and doesn't touch the common-case fast path.
