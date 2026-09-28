# STRESS-003: Concurrent scans of the same project

**Epic:** Full-Loop Stress Testing
**Status:** done
**Depends on:** none (can reuse STRESS-001's project, or a smaller one — see Design)
**Estimated size:** medium

## Goal
Fire two `ragctl scan` invocations at the same project directory at (as close to) the same instant as possible, and confirm `Scheduler.LockProject` actually serializes the persist step under real OS-level concurrency — not just under the existing unit test (`internal/daemon/scheduler_test.go`), which exercises the lock in-process with synthetic timing, never two genuinely separate `scan` processes racing a real daemon over its real HTTP socket.

## Non-goals
- No fix to `LockProject` itself unless this finds a real race — this is a verification ticket first.
- No stress on `scan`'s resolver step (that's inherently safe to run twice; the risk is specifically in the shared persist step both scans compete for).

## Simplicity constraints
- A small, fast-resolving project is fine here (unlike STRESS-001, which needs real scale) — the point is race timing, not resolution volume. Use STRESS-001's project only if convenient; a small fixture with a deliberately slow resolver step (or an artificial small delay, if one can be added without touching production code) makes the race window easier to hit reliably.

## Design
1. Start the real daemon.
2. Launch two `ragctl scan <path>` processes as close to simultaneously as the shell allows (`&` backgrounding both, or a tiny driver script that starts both with no delay between).
3. Wait for both to exit; capture both exit codes and stdout.
4. Confirm: exactly one project registration exists afterward (not two, not a corrupted merge of both), `ragctl project list` shows one entry, and the resolution is complete and correct.
5. Repeat the two-concurrent-scan race 10+ times to increase the chance of hitting a genuine timing window, not just the common case where one process happens to finish first with room to spare.

## Inputs / Outputs
- Input: one project directory, two concurrent `scan` invocations.
- Output: pass/fail on exactly-one-registration and resolution correctness, across multiple repeated race attempts.

## Failure behavior
- A duplicate/corrupted project registration, a partial resolution, or a daemon crash under this race is this ticket's real finding — reproduce it reliably before filing a fix ticket, since a race bug needs a repeatable trigger to actually verify a fix against.

## Tests
- Manual/live exercise, ideally scripted (a small shell loop) so it can be re-run easily if a fix is later attempted.

## Acceptance criteria
- [x] Two genuinely concurrent `scan` processes against the same project directory never produce a duplicate or corrupted project registration, across 10+ repeated attempts.
- [x] Any race found is reliably reproducible, not a one-off flake report. (N/A — no race found.)

## Post-implementation note

Ran via `hack/test-linux.sh`'s Podman pattern. Used a small hand-built fixture per the ticket's own Simplicity constraint (`example.com/raceproj`, `go.mod` requiring real `github.com/spf13/cobra` + `github.com/google/uuid`) rather than STRESS-001's terraform clone — real network resolution but fast enough to repeat the race many times in one session. Started the daemon explicitly first (`ragctl daemon start`), then fired 12 iterations of two genuinely separate `ragctl scan` OS processes backgrounded with no delay between them (`&` + `wait`), checking `ragctl project list`'s count after every iteration.

**Result: 12/12 clean.** Every iteration produced exactly one project registration, both processes always exited 0, and the final resolved dependency list was correct and complete (cobra + uuid + their real transitive deps, direct/indirect correctly flagged) — no corruption, no daemon crash, no duplicate. `Scheduler.LockProject` serializes the persist step correctly under real OS-level concurrency, not just the existing in-process unit test's synthetic timing. No race found; no follow-up needed.

**Re-verified 2026-09-27 — no regression.** Same fixture shape (a fresh `example.com/raceproj` requiring real `github.com/spf13/cobra` + `github.com/google/uuid`), 12 iterations of two genuinely separate `ragctl scan` OS processes backgrounded with no delay between them, checking `ragctl project list`'s matching-project count after every iteration. **Result: 12/12 clean** — both processes exited 0 every time, exactly one `raceproj` registration after every iteration, and the final resolved dependency list was correct and complete (8 real transitive dependencies, direct/indirect correctly flagged). `Scheduler.LockProject` still serializes correctly under real OS-level concurrency. Verdict: **no regression.**
