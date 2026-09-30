# STRESS-018: Full-loop chaos loop

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** STRESS-001 through STRESS-017 (this is the combined exercise; earlier tickets found and (where small) fixed the individual-stage issues this ticket would otherwise rediscover one at a time)
**Estimated size:** large

## Goal
Repeatedly run the full `scan → sync → gc → serve` loop against a real project, with a random `kill -9` injected at a random point during each cycle, many iterations — the single highest-value test in this epic, since it's the only one that exercises the interaction between every stage under real adversarial conditions at once, not each stage in isolation. After every kill+restart, `ragctl doctor` must report clean, and the next full cycle must still complete correctly.

## Non-goals
- No new chaos/fault-injection framework — a small driver script (bash or Go) that picks a random delay before `kill -9`-ing the daemon PID is enough; this doesn't need to become general infrastructure.
- No fixing of anything found in this ticket itself beyond what's small/obvious — a real bug found here is a release-blocking finding, filed as its own ticket immediately (matching every other ticket in this epic's own convention).

## Simplicity constraints
- Use a **small-but-real** fixture project for this ticket specifically, not STRESS-001's large one (`terraform`/`prometheus`) — 20+ full-loop iterations against a 150-dependency project would make each iteration impractically slow. A project requiring just **`github.com/spf13/cobra`** and **`github.com/google/uuid`** (two real, fast-to-sync dependencies — enough for a real, non-trivial `sync`/`gc`/`serve` cycle) keeps each iteration fast enough to run 20+ times in one session.
- Reuse everything else already built: STRESS-006's kill-timing approach, `ragctl doctor` as the ground-truth health check after every restart.

## Design
A driver script that, per iteration:
1. Picks a random point in the loop to kill at (before/during scan, before/during sync, before/during gc, before/during a serve tool call) and a random delay within that stage.
2. Starts the daemon, runs `scan → sync → gc → serve` (one real MCP tool call to close the loop) against the fixture project.
3. At the chosen random point, `kill -9` the daemon.
4. Restarts the daemon.
5. Runs `ragctl doctor` — must report clean. If not, stop immediately and record the exact iteration/kill-point that produced the failure (a chaos test's value depends entirely on being able to reproduce what it found).
6. If `doctor` is clean, runs one more full `scan → sync → gc → serve` cycle to confirm the system is genuinely still functional, not just superficially "not crashed."
7. Repeats for N iterations (start with N=20; increase if time allows and nothing's been found yet).

## Inputs / Outputs
- Input: N chaos iterations against a real project, each with a randomly-timed `kill -9`.
- Output: pass/fail per iteration (`doctor` clean + a working next cycle), and for any failure, the exact reproducing iteration/kill-point recorded precisely enough to redrive it deterministically for a fix attempt.

## Failure behavior
- Any iteration where `doctor` reports unclean, or where the next cycle fails, is a release-blocking finding — stop the loop, record the exact reproduction steps, file it as its own ticket immediately rather than continuing to accumulate more (possibly related) failures on top of an already-broken state.

## Tests
- The chaos driver script itself is the test; not part of `go test ./...` (real process kills, real timing, not something to run in normal CI).

## Acceptance criteria
- [x] N (≥20) full-loop chaos iterations, each with a randomly-timed `kill -9`, all end with `ragctl doctor` reporting clean and a subsequent full cycle completing correctly.
- [x] Any failure found is reliably reproducible via the exact recorded iteration/kill-point, not a one-off unreproducible flake report.

## Post-implementation note (2026-09-29)

Built an isolated fixture (real Qdrant, isolated `$HOME`, `watch.enabled: false`, `retention.grace_period: 5s`) with the ticket's own specified small-but-real fixture (`github.com/spf13/cobra` + `github.com/google/uuid`, 8 total dependencies) and a Go chaos driver (`hack/stress018/main.go`, deleted after use) implementing the exact design above: per iteration, randomly pick one of the four stages (`scan`/`sync`/`gc`/`serve`, the last a real MCP client call), start that stage command as a subprocess, wait a random delay scaled to that stage's real typical duration, verify the daemon PID's command line actually matches our isolated scratch path (safety protocol) before `kill -9`-ing it, restart the daemon, run `ragctl doctor`, and — if clean — run one additional full cycle to completion with no kill to confirm genuine functionality, not just a superficial "didn't crash."

Ran **two full batches of 20 iterations each (40 total)**, spanning all four stages with kill delays ranging from 2ms to 1.4s (deliberately landing well inside real operations for the smaller/faster stages, and inside real git-clone/embed work for `sync`). **All 40 iterations passed**: every single kill+restart left `ragctl doctor` reporting clean, and every subsequent full `scan → sync → gc → serve` cycle completed correctly with no corruption, no hang, and no degraded state carried forward into the next iteration.

This is a genuinely strong, positive result for the store-integrity and recovery work this session's earlier tickets built and fixed (`OPS-003/004/005`'s doctor checks and `--rebuild` remedy, `STORE-004`'s cross-store restart guarantee, atomic promotion from `VALID-001`) — none of that work was undone or found lacking under combined, randomized, real adversarial conditions hitting every stage of the loop, not just one stage in isolation. No new finding to file; this closes out the epic's own highest-value, deliberately-last test with a clean result.

Real production untouched throughout (fully isolated fixture and infrastructure); all isolated infra (daemon, `ragctl-stress018` podman container, scratch directory) cleaned up after the run.
