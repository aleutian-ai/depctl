# STRESS-007: Concurrent cross-project sync against a real daemon

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
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
3. Fire both projects' `depctl sync` calls as close to simultaneously as possible (background both from the shell).
4. Confirm both complete successfully — note (2026-09-29, updated before running): epic 53 (`BuildCoordinator`) means two *different* generations (different dependency versions) no longer serialize against each other at all, only identical-generation builds coalesce via singleflight; so this now exercises genuine concurrent building of two versions of the same dependency, a stronger test than the originally-envisioned serialized case. No crash, no interleaved-write corruption either way.
5. Query each project via `explain_call_site`/`search_dependency_docs` scoped to its own version — confirm each returns only its own version's content, exactly like VALID-002's fixture-scale test, now under real concurrent daemon load.

## Inputs / Outputs
- Input: two projects, one real shared dependency at two real versions, concurrent sync requests.
- Output: pass/fail on both syncs completing correctly and both projects' searches remaining correctly version-isolated.

## Failure behavior
- Any cross-version leak or sync corruption under this real-concurrency scenario is a release-blocking finding — file immediately.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] Two concurrent real syncs of the same dependency at different versions, for two different projects, both complete correctly.
- [x] Each project's search remains correctly scoped to its own resolved version afterward, matching VALID-002's guarantee under real daemon concurrency.

## Post-implementation note (2026-09-29)

Built an isolated fixture (real Qdrant, isolated `$HOME`, `watch.enabled: false`) with two real projects each depending on `google.golang.org/grpc` at a different real tag (`v1.60.0` and `v1.68.0`, 28 and 26 total dependencies respectively) and fired both projects' real `depctl sync` as two genuinely separate client processes within ~0ms of each other, confirmed via `ps` to actually overlap in wall-clock time (28m24s and 22m30s, run concurrently, not summed).

**Real finding #1 (`OPS-007`, fixed same-day):** project A failed on `cloud.google.com/go/compute/metadata v0.2.3` with a real `git clone --mirror` collision (`fatal: cannot copy '.../hooks/commit-msg.sample': File exists`) — both projects depend on different tags of the same underlying `github.com/googleapis/google-cloud-go` monorepo, and the two concurrent syncs raced on that repository's shared mirror directory. Root-caused to `RunSync` building a fresh `git.Cache` (and therefore a fresh, empty `mirrorLocks` map) on every call, defeating `EnsureMirror`'s own in-process lock, which was specifically designed to prevent exactly this — only reachable once epic 53 removed cross-project sync serialization. Fixed by hoisting `git.Cache` construction to once per daemon lifetime (`engine.gitCache`), matching the existing `syncSem` pattern; full `go build`/`go vet`/`go test ./...` pass, and the specific failed dependency rebuilt cleanly afterward with the fix in place. (The exact mirror-creation race itself wasn't re-forced live under the fix in this same environment, since the mirror already existed from the first, buggy run by the time the fixed binary was tested — the fix's mechanism was verified by build/test/code-review, not a second live collision.)

**Real finding #2 (not a bug, a real symptom worth recording):** both projects also independently hit real `embed batch: embedding cache: inner embed: context deadline exceeded` failures (4 in A, 1 in B) — two large, genuinely concurrent real embedding workloads against the one shared local Ollama instance produced real request timeouts under that combined real load. All cleared cleanly on retry (`--rebuild --dependency <name>`, per this session's own established OPS-004/005 recovery pattern) once the concurrent load eased. Not itself a depctl bug — real evidence that two large concurrent syncs can genuinely saturate a single local embedder, worth knowing for anyone running depctl against a resource-constrained embedding backend, but out of this ticket's scope to fix (no non-goal violated).

**The actual guarantee this ticket exists to verify — confirmed correct, with one non-obvious real detail:** `depctl describe`'s single-pointer "active version" display only ever shows ONE version per dependency name (confirmed via `activeGenerationKey`'s `(ecosystem, name, backend)` keying, no version component) — briefly looked like it might mean only the most-recently-promoted version could ever be searched correctly, which would have been a serious, real regression in the cross-project isolation guarantee. Directly tested via a real `search_dependency_docs` call from each project's own context: **both returned correct, version-appropriate content with different generation IDs** (`gen_...NX1C...` for A's `v1.60.0` build, `gen_...B3A0...` for B's `v1.68.0` build) despite `describe` only displaying one of them — confirming VALID-002/FIX-002's per-project version-scoped resolution genuinely holds under real concurrent daemon load, and that `describe`'s single-pointer view is just a display simplification, not a reflection of what search actually uses.

`depctl doctor` reported clean (19 ok, 0 warning, 0 unhealthy) after all retries completed.
