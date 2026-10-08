# STRESS-009: Large single-dependency blobless-clone stress

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** none
**Estimated size:** medium

## Goal
Sync one real dependency with a genuinely large git history/object count against a real remote, to stress-test GIT-005's blobless clone (`--filter=blob:none`) and sparse-checkout path at the scale that actually matters — a small local fixture repo can't exercise "does this avoid fetching the blobs for a repo with tens of thousands of commits and a large binary/vendor history," only a real large remote can.

## Non-goals
- No comparison against a non-blobless clone in this ticket beyond what's needed to show the improvement (i.e. don't build a full A/B benchmarking harness — a single before/after `du`/timing comparison is enough).

## Simplicity constraints
- Use **`k8s.io/kubernetes`** (`github.com/kubernetes/kubernetes`) as the primary target — one of the largest, most actively-developed real Go repositories, with a genuinely large commit/object history. If a full clone attempt proves impractically slow/large even with the blobless filter (network/disk constraints in the test environment), fall back to **`github.com/moby/moby`** (module `github.com/docker/docker`) — still large, meaningfully smaller.

## Design
1. Real daemon, real network.
2. `depctl sync --dependency k8s.io/kubernetes` (or `github.com/docker/docker` if using the fallback) against a project resolving it.
3. Measure: wall-clock, and `du -sh` of the resulting local git mirror directory (`<data-dir>/git/...`) — compare against what a full, non-blobless `git clone` of the same repository would occupy (a quick `git clone https://github.com/kubernetes/kubernetes` — or `https://github.com/moby/moby` for the fallback — into a scratch dir for direct comparison, not run through depctl).
4. Confirm the synced content is still correct (real chunks, real search results) — GIT-005's sparse-checkout fallback-to-full-checkout path must never silently produce empty/wrong content for a large real repo, only for the small ones already covered by existing tests.

## Inputs / Outputs
- Input: one real, large-history dependency.
- Output: recorded wall-clock and disk-usage comparison (depctl's blobless+sparse clone vs. a full `git clone` of the same repo), plus a correctness check on the resulting content.

## Failure behavior
- Network/disk-space-dependent — skip if the chosen dependency's full history is impractically large for the environment running this test.
- Any correctness gap (empty/wrong content for this specific large repo) is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] A real, large-history dependency syncs correctly via the blobless/sparse-checkout path.
- [x] Disk usage is measurably smaller than a full `git clone` of the same repository, recorded as a concrete number.

## Post-implementation note (2026-09-29)

Used the ticket's own sanctioned fallback, `github.com/docker/docker` (moby/moby, resolved at `v24.0.9+incompatible`), rather than `k8s.io/kubernetes`: kubernetes isn't practically importable as a plain Go dependency for a minimal fixture without dragging in an enormous, unrepresentative transitive graph just to get `go mod tidy` to resolve — moby/moby is both a genuinely large, long-history, actively-developed real repository (58,225 commits) and a commonly-imported real dependency, matching the ticket's own criteria without that practical obstacle. (Note: moby/moby's module path itself has since moved to `github.com/moby/moby` for current versions — the older, pre-rename `v24.0.9+incompatible` tag still resolves cleanly as `github.com/docker/docker`.)

Built an isolated fixture (real Qdrant, isolated `$HOME`) importing `github.com/docker/docker/api/types/container`, scanned, and ran a real `depctl sync --dependency github.com/docker/docker` against the real network: **1m05s wall-clock**, `1 synced, 0 failed, 0 skipped`.

**Disk usage comparison (the ticket's core measurement):**
- depctl's blobless+sparse git mirror: **126MB**.
- A real, full, non-blobless `git clone https://github.com/moby/moby.git` of the same repository (run directly, not through depctl, for comparison): **424MB total** (290MB `.git` + 134MB checked-out working tree; 58,225 commits, 494,465 objects, 288MB packed).
- **Roughly 3.4x smaller overall** (126MB vs. 424MB), or **~2.3x smaller** comparing bare-repo to bare-repo (126MB vs. 290MB `.git`) — a real, measured confirmation that GIT-005's blobless+sparse-checkout approach meaningfully reduces disk usage at the scale that actually matters, not just on small fixture repos.

**Correctness confirmed**: `depctl describe` showed a complete backend replica (5355 chunks, `status=complete`), and a real `search_dependency_docs` call returned real, version-correct content (`version: v24.0.9+incompatible`, matching the resolved dependency) — GIT-005's sparse-checkout path did not silently produce empty or wrong content at this real scale. `depctl doctor` reported clean (19 ok, 0 warning, 0 unhealthy) throughout.
