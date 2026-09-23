# STRESS-006: Kill the daemon mid-sync

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none (a small fixture project is enough; STRESS-001's project works too if a longer sync window is wanted)
**Estimated size:** medium

## Goal
`kill -9` the daemon process while a real sync is actively running (not a graceful `ragctl daemon stop`), then restart it, and confirm: no corrupted bbolt/Badger state (both stores still open and readable), the interrupted generation is left in a sane, inspectable state (feeding correctly into FIX-001's `FAILED` transition where applicable, or a genuinely orphaned non-terminal state that GC-001's orphan planner can see), and a subsequent `ragctl sync` re-run completes normally with no lingering corruption.

## Non-goals
- No graceful-shutdown testing — that path (`ragctl daemon stop`) is already covered by existing scheduler tests. This ticket is specifically about the ungraceful case, `kill -9`, which bypasses `Scheduler.Shutdown` entirely.
- No fix for whatever's found beyond confirming the system's actual behavior — a real corruption bug found here is a release-blocking finding, filed as its own ticket immediately, not patched inline under time pressure.

## Simplicity constraints
- Use **`github.com/grpc/grpc-go`** (module `google.golang.org/grpc`) as the kill target — already this codebase's own running example throughout `docs/`, real, multi-file, with a non-trivial clone+embed duration (a few seconds at least) so there's a real window to land the kill inside `ACQUIRING`/`NORMALIZING`/`INDEXING`/`VALIDATING`, not just before or after the whole action.

## Design
1. Start the daemon, begin `ragctl sync --dependency google.golang.org/grpc` against a project resolving `github.com/grpc/grpc-go`.
2. From a second terminal, `kill -9 <daemon pid>` at a moment estimated to land mid-`ACQUIRING`/`REPLICATE` (timing by trial — run once first to see how long the target dependency takes, then kill partway through on the next attempt).
3. Confirm the `sync` client process itself reports a connection-lost error, not a hang.
4. Restart the daemon (`ragctl daemon status`/autostart via any client command).
5. `ragctl doctor` — must report a clean, non-corrupted state (bbolt/Badger both open successfully, schema version intact).
6. Inspect the interrupted generation's state directly (`ragctl describe` or a direct store read) — record what state it's actually in.
7. `ragctl gc --orphans --dry-run` — confirm the interrupted generation (if left in a FAILED/stuck-non-terminal state) is correctly detected as a candidate.
8. Re-run `ragctl sync` for the same dependency — confirm it completes normally, without needing any manual cleanup.
9. Repeat steps 1-8 at least 3 times with the kill landing at different points in the pipeline (during acquisition, during embedding, during the final promotion write) to cover more of the failure-window space than one lucky/unlucky timing.

## Inputs / Outputs
- Input: a project with a real, multi-second sync target; a `kill -9` landed mid-sync.
- Output: pass/fail on store integrity, generation state sanity, orphan-GC detection, and clean re-sync — across at least 3 differently-timed kills.

## Failure behavior
- Any store corruption (bbolt/Badger failing to open, `doctor` reporting a real problem, a re-sync that fails or produces wrong results) is this ticket's real finding — a release-blocking bug, filed immediately as its own ticket.

## Tests
- Manual/live exercise; ideally scripted so the same kill-timing scenarios can be re-run after any fix.

## Acceptance criteria
- [ ] A `kill -9` mid-sync, at three different pipeline points, never corrupts bbolt or Badger.
- [ ] `ragctl doctor` reports clean after every restart.
- [ ] The interrupted generation is either cleanly resumable or correctly detected by orphan GC — never silently invisible.
- [ ] A re-sync after each kill completes normally.
