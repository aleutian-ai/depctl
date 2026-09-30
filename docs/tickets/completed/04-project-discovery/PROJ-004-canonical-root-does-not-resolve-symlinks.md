# PROJ-004: Canonical project root doesn't resolve symlinks

**Epic:** Project Discovery
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** small

## Goal
Found live during epic 49's STRESS-008 (JIT-sync storm): scanning the exact same physical directory via two different (but semantically identical) path spellings produced **two different registered projects with two different project IDs**. Twelve concurrent `ragctl serve` subprocesses (one per simulated agent session, each with `cmd.Dir` set to the fixture path) each ran MCP-006's startup scan (`scan.ScanProject(ctx, ".", nil)`), which registered the project at its realpath (`/private/tmp/stress008-.../fixture` on this macOS test machine). A separate, earlier `ragctl scan /tmp/stress008-.../fixture` (the literal, absolute path given, through macOS's symlinked `/tmp -> /private/tmp`) had already registered the *same* directory as a different project. `ragctl project list` showed both as fully independent registrations, each with their own dependency references and generations for the identical 27-dependency graph.

Root cause, confirmed by reading the code: `internal/project/id.go`'s `ProjectID` is a pure hash of whatever canonical string it's given; the actual normalization is `internal/project/scanner.go`'s `canonicalize()`, which was only `filepath.Abs` + `filepath.Clean` — **no `filepath.EvalSymlinks` anywhere**. `filepath.Abs` on an already-absolute path (`ragctl scan /tmp/foo`, the literal path a user or agent gives) is a no-op `Clean`, preserving any symlink component verbatim. But `filepath.Abs(".")` (what `startupScan`'s relative `"."` argument produces) calls `os.Getwd()`, which falls back to the kernel's `getcwd()` syscall whenever `$PWD` is unset or stale — a real possibility for a daemon/MCP-spawned subprocess — and the kernel's `getcwd()` always returns the fully symlink-resolved path. The two call paths (`ragctl scan <literal path>` vs. `ragctl serve`'s own `"."`-relative auto-scan) can therefore silently diverge into two different canonical roots for the identical directory, whenever that directory sits under a symlinked ancestor — not just macOS's `/tmp`, but any real-world symlinked home directory, cloud-sync folder, or monorepo tooling symlink.

PROJ-002's own original design (`internal/project/scanner.go`'s predecessor spec) explicitly scoped out *case-insensitive-filesystem* normalization as a documented non-goal, but never considered symlink resolution as a separate concern — this is a genuine gap in the original design, not something deliberately declined.

## Non-goals
- No change to `ProjectID`'s hash function or ID format — the fix is entirely in what string gets hashed, not how.
- No retroactive migration/merge of any already-duplicated real projects this bug may have caused in production — v0.1's existing "moving a project produces a new ID, old references aren't auto-transferred" policy (PROJ-002) already covers the recovery path (re-scan under one consistent spelling going forward; stale duplicates age out via GC's existing reference/grace-period mechanics like any other stale project).

## Simplicity constraints
- One-line fix at the single shared normalization point (`canonicalize`) — both call paths (`ragctl scan`, `startupScan`'s `"."`) already funnel through it, so fixing it there fixes both without touching either caller.

## Design
`internal/project/scanner.go`'s `canonicalize` now resolves `filepath.EvalSymlinks` on the `Abs`+`Clean` result, falling back to the unresolved path if `EvalSymlinks` fails (a dangling symlink or a permissions error on an ancestor) — non-fatal, matching the project's existing "degrade gracefully rather than fail the whole operation over a display-path oddity" posture elsewhere.

## Inputs / Outputs
- Input: the same physical directory, referred to by two different path spellings (one traversing a symlink, one already resolved).
- Output: one canonical root, one project ID, regardless of which spelling was used.

## Failure behavior
- Any remaining divergence between the two call paths for the same physical directory is this ticket's finding.

## Tests
- `TestCanonicalRootResolvesSymlinkedAncestor` (new, `internal/project/id_test.go`): creates a real symlink via `os.Symlink` (not relying on macOS's own `/tmp`, for portability) and confirms both the symlinked and real spellings resolve to the same canonical root and `ProjectID`. Confirmed to fail against the pre-fix `canonicalize` (reverted via `git stash` and re-run) and pass with the fix.
- Fixed a downstream test-fixture assumption this surfaced: `internal/project/scanner_test.go`'s `buildFixtureTree` used `t.TempDir()`'s raw path directly, which on macOS is itself under the symlinked `/var -> /private/var` — now resolves it the same way, matching the well-known Go testing convention for this exact platform quirk.
- Same fix applied to `internal/cli/plan_test.go`'s shared `scanDepFixture` helper, which several other `internal/cli` tests (`TestEngineProjectsListsResolvedProjects`, `TestEngineSyncResolvesFirstWhenAsked`, `TestDaemonResyncsProjectWhenGoModChanges`, `TestDaemonQueryServiceRoundTripsThroughRealDaemon`, `TestRunSyncNoopActionsDoNotCountAsProgress`, `TestSyncOfflineSkipsSyncVersionButRecordsReference`, `TestSyncNoOpPlanMakesNoNetworkCalls`) depend on — all 7 failed against the fix until this one shared helper was corrected, then all passed with no other changes needed.
- Full `go build ./...`, `go vet ./...`, and `go test ./...` clean after all three fixes.

## Acceptance criteria
- [x] The same physical directory, scanned via a literal symlinked path and via a realpath-resolving relative path, produces one canonical root and one project ID.
- [x] A regression test proves this and is confirmed to fail against the pre-fix code.
- [x] Existing tests whose fixtures happened to rely on the old (symlink-preserving) behavior are corrected, not worked around — `go test ./...` fully clean.

## Post-implementation note (2026-09-29)

Fixed exactly as scoped. `internal/project/scanner.go`'s `canonicalize` gained an `EvalSymlinks` step after `Abs`+`Clean`. Fixing it surfaced (and required fixing) three pre-existing test fixtures that had been silently relying on macOS's `/var`/`tmp` symlink NOT being resolved — each was a `t.TempDir()`-derived root compared directly against a scan result's now-correctly-resolved `Root`, all fixed by resolving the expected root the same way at the one place each was built (`scanner_test.go`'s `buildFixtureTree`, `internal/cli/plan_test.go`'s `scanDepFixture`). No other test or production code needed changes. This was found incidentally while setting up STRESS-008's own isolated fixture (a scratch directory under `/tmp`) — the JIT-sync-storm test itself is written up separately in its own ticket.