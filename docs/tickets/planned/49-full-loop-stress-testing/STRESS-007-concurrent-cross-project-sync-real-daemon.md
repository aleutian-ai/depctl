# STRESS-007: Concurrent cross-project sync against a real daemon

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none
**Estimated size:** medium

## Goal
Re-run VALID-002's cross-project version-isolation scenario (two projects, one shared dependency, two different resolved versions) against a **real daemon, real embedder, and real vector backend**, with both projects' syncs genuinely running concurrently (not sequentially, as VALID-002's own test does with an in-process fake backend) — confirming the promotion/supersede mechanics and the version-filtered search guarantee both hold under actual concurrent daemon load, not just in-process test doubles with instant, sequential fakes.

## Non-goals
- No new isolation logic — this is a real-infrastructure re-verification of an already-proven (at fixture scale) guarantee, not new scope.
- No fix unless this finds a real gap between fixture-scale and real-scale behavior.

## Simplicity constraints
- Reuse **`github.com/grpc/grpc-go`** (module `google.golang.org/grpc`) — already this codebase's own running example, with real, meaningfully different tagged versions (e.g. `v1.60.0` and `v1.68.0`) and a fast-enough sync time to keep this test quick.

## Design
1. Real daemon, real Qdrant/Ollama.
2. Register Project A resolving `google.golang.org/grpc@v1.60.0`, Project B resolving `google.golang.org/grpc@v1.68.0` (two real, meaningfully different tagged versions of the same real dependency).
3. Fire both projects' `ragctl sync` calls as close to simultaneously as possible (background both from the shell).
4. Confirm both complete successfully (the daemon's global lock serializes them; no crash, no interleaved-write corruption).
5. Query each project via `explain_call_site`/`search_dependency_docs` scoped to its own version — confirm each returns only its own version's content, exactly like VALID-002's fixture-scale test, now under real concurrent daemon load.

## Inputs / Outputs
- Input: two projects, one real shared dependency at two real versions, concurrent sync requests.
- Output: pass/fail on both syncs completing correctly and both projects' searches remaining correctly version-isolated.

## Failure behavior
- Any cross-version leak or sync corruption under this real-concurrency scenario is a release-blocking finding — file immediately.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] Two concurrent real syncs of the same dependency at different versions, for two different projects, both complete correctly.
- [ ] Each project's search remains correctly scoped to its own resolved version afterward, matching VALID-002's guarantee under real daemon concurrency.
