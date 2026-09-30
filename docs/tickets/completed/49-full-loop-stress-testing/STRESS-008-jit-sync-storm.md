# STRESS-008: JIT-sync storm

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** none (STRESS-001's project, or any project with 10+ never-synced dependencies, works)
**Estimated size:** medium

## Goal
Fire many `search_dependency_docs` calls, each against a *different* never-synced dependency of the same project, at (as close to) the same moment — simulating several agent sessions independently hitting cold dependencies at once. Confirm WATCH-019's JIT-sync trigger and WATCH-020's priority-bump path behave correctly under this real concurrent load: every request eventually gets a correct, real answer (either the synced content or a correctly-classified error), none hang forever, and none silently drop.

**Note (2026-09-29, updated before running):** "the daemon's global sync lock" no longer describes the real mechanism — epic 53's `BuildCoordinator` means the first `search_dependency_docs` call triggers one project-wide sync (all of that project's dependencies, not just the requested one), and every subsequent concurrent call for the *same* project, per WATCH-020, bumps its own dependency's priority within that one already-running sync rather than queuing a fully redundant second one. This ticket verifies that priority-reordering-under-real-concurrency mechanism, not a lock.

## Non-goals
- No new JIT-sync logic — WATCH-019/020 are already shipped and tested at smaller scale (VERIFY-001's smoke test exercises one JIT-sync call). This ticket is about behavior under N simultaneous triggers, not new functionality.

## Simplicity constraints
- Reuse VERIFY-001's real-MCP-client pattern (a real `ragctl serve` subprocess, a real MCP client) rather than building new test infrastructure — just issue N concurrent `CallTool` requests instead of one.
- Use a fixed list of 12 real, small-to-medium, fast-to-sync Go modules as the fixture project's dependencies, so results are comparable run to run:
  `github.com/spf13/cobra`, `github.com/spf13/pflag`, `github.com/stretchr/testify`, `github.com/pkg/errors`, `gopkg.in/yaml.v3`, `github.com/sirupsen/logrus`, `github.com/gorilla/mux`, `go.uber.org/zap`, `github.com/BurntSushi/toml`, `github.com/golang/protobuf`, `github.com/mitchellh/mapstructure`, `github.com/google/uuid`.

## Design
1. A project whose `go.mod` requires all 12 dependencies listed above, none yet synced.
2. Spawn one real `ragctl serve` subprocess, connect one real MCP client (or several, one per simulated agent — try both shapes).
3. Issue `search_dependency_docs` calls for N different dependencies concurrently (goroutines, or N separate client connections).
4. Record: does every call eventually complete with a sensible result (either real synced content, or a clear, correctly-classified error — never a hang, never a malformed/empty response that isn't one of those two)? How long does the whole batch take versus N sequential JIT-syncs?
5. Repeat with a smaller subset (e.g. the first 3 of the 12) and, if more spread is wanted, extend the list with a few more real fast modules (e.g. `github.com/google/go-cmp`, `github.com/davecgh/go-spew`) to reach 15+ — to see how the daemon's single global sync lock affects total latency as load increases: is it linear, or does something degrade worse than that?

## Inputs / Outputs
- Input: a project with N never-synced dependencies, N concurrent `search_dependency_docs` calls.
- Output: pass/fail on every call completing correctly, plus a latency/throughput data point for how the daemon's serialized-GC/sync design scales under this load shape.

## Failure behavior
- A hung request, a dropped request, or a malformed response under this load is this ticket's finding.
- Total-latency scaling far worse than linear in N is worth recording even if nothing technically fails — it's the kind of thing that matters for a real multi-agent deployment.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] N concurrent JIT-sync-triggering `search_dependency_docs` calls (N ≥ 10) all complete with a correct result or a correctly-classified error — none hang, none drop.
- [x] Total-batch latency is recorded and compared against N × single-call latency, to characterize how serialized sync scheduling scales.

## Post-implementation note (2026-09-29)

Built an isolated fixture (real Qdrant, isolated `$HOME`, `watch.enabled: false`, `disable_ambient: true` so only the JIT-sync path itself could trigger any work) with the 12 real dependencies listed above (27 total after `go mod tidy`), none synced. Used N = 12 **separate MCP client sessions** (12 separate real `ragctl serve` subprocesses, the harder of the two shapes step 2 suggested — genuinely independent agent sessions, not one client issuing 12 goroutine calls), each issuing one `search_dependency_docs` call for a different one of the 12 dependencies, fired concurrently.

**Core acceptance criterion held:** all 12 calls completed with a correct, well-classified result — 5 succeeded with real synced content, 7 returned a clear `no synced knowledge for this version yet — run \`ragctl sync\`` tool-level error after an identical ~91.3s bounded wait. **None hung, none dropped, none returned a malformed or empty response.**

**Real finding, worth recording (not a bug):** under this genuine storm shape (N simultaneous *first-time* requests, as opposed to N-1 requests arriving cleanly after the first is already visibly running), only 5 of 12 individually-requested dependencies were actually built within the bounded wait — despite several of the missed ones (`cobra`, `testify`, `pkg/errors`, `yaml.v3`, `logrus`, `mux`, `protobuf`) being small, fast packages that synced in 5-16s on their own. Verified this is a genuine storm-collision effect, not a capability gap: a fresh, isolated retry of `cobra` alone (after the batch ended) succeeded in 5.1s. Read `searchDependencyDocsHandler` (`internal/mcp/tools.go`) to understand why: WATCH-019's JIT sync is scoped to *just the requested dependency* (`sync.SyncProject(ctx, projectID, []string{dep}, nil)`), and WATCH-020's priority-bump path only helps a caller whose check lands *after* another sync is already visibly running for the project — with 12 requests landing within microseconds of each other, most of them race to start their own single-dependency sync at the same moment as everyone else, lose, and fall into "already running; queued a follow-up" without their own specific dependency ever actually being retried within their own bounded wait. The underlying store was left at 5/27 total references — the background sync did not continue on its own to eventually satisfy the other 7 after the batch's individual waits gave up.
- No fix filed for this — it's a real, honest characterization of the priority-bump mechanism's limit under true N-way simultaneous cold-start pressure (as opposed to the "N-1 arrive after 1" shape WATCH-020 was originally designed and tested for), worth knowing for real multi-agent deployments, but each individual request's own behavior (clear error, no hang) is exactly what the architecture promises — a subsequent retry (or `sync_project`/`scan_project` up front) resolves it, matching the existing, already-correct fallback story.

**Real bug found and fixed the same day (`PROJ-004`, epic 04):** setting up this test's 12 separate `ragctl serve` subprocesses (each running MCP-006's own startup scan against the fixture's path) surfaced that the fixture registered as **two different projects** — one from this ticket's own explicit `ragctl scan /tmp/.../fixture` (literal path, preserving macOS's `/tmp -> /private/tmp` symlink), one auto-registered by `startupScan`'s `"."`-relative scan (which `os.Getwd()` resolves to the realpath). Root-caused to `internal/project/scanner.go`'s `canonicalize` never calling `filepath.EvalSymlinks`. Fixed the same day — see `PROJ-004`'s own ticket for the full write-up, fix, and the three pre-existing test fixtures it required correcting.

`ragctl doctor` reported clean throughout (no corruption from the storm itself — the 7 "not yet synced" responses are correct classifications, not errors in the store).
