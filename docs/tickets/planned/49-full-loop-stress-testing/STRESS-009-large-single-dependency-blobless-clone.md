# STRESS-009: Large single-dependency blobless-clone stress

**Epic:** Full-Loop Stress Testing
**Status:** planned
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
2. `ragctl sync --dependency k8s.io/kubernetes` (or `github.com/docker/docker` if using the fallback) against a project resolving it.
3. Measure: wall-clock, and `du -sh` of the resulting local git mirror directory (`<data-dir>/git/...`) — compare against what a full, non-blobless `git clone` of the same repository would occupy (a quick `git clone https://github.com/kubernetes/kubernetes` — or `https://github.com/moby/moby` for the fallback — into a scratch dir for direct comparison, not run through ragctl).
4. Confirm the synced content is still correct (real chunks, real search results) — GIT-005's sparse-checkout fallback-to-full-checkout path must never silently produce empty/wrong content for a large real repo, only for the small ones already covered by existing tests.

## Inputs / Outputs
- Input: one real, large-history dependency.
- Output: recorded wall-clock and disk-usage comparison (ragctl's blobless+sparse clone vs. a full `git clone` of the same repo), plus a correctness check on the resulting content.

## Failure behavior
- Network/disk-space-dependent — skip if the chosen dependency's full history is impractically large for the environment running this test.
- Any correctness gap (empty/wrong content for this specific large repo) is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] A real, large-history dependency syncs correctly via the blobless/sparse-checkout path.
- [ ] Disk usage is measurably smaller than a full `git clone` of the same repository, recorded as a concrete number.
