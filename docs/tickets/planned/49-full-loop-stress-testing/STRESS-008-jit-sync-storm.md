# STRESS-008: JIT-sync storm

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none (STRESS-001's project, or any project with 10+ never-synced dependencies, works)
**Estimated size:** medium

## Goal
Fire many `search_dependency_docs` calls, each against a *different* never-synced dependency of the same project, at (as close to) the same moment — simulating several agent sessions independently hitting cold dependencies at once. Confirm WATCH-019's JIT-sync trigger, the daemon's global sync lock, and WATCH-020's priority-bump path all behave correctly under this real concurrent load: every request eventually gets a correct, real answer (either the synced content or a correctly-classified error), none hang forever, and none silently drop.

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
- [ ] N concurrent JIT-sync-triggering `search_dependency_docs` calls (N ≥ 10) all complete with a correct result or a correctly-classified error — none hang, none drop.
- [ ] Total-batch latency is recorded and compared against N × single-call latency, to characterize how serialized sync scheduling scales.
