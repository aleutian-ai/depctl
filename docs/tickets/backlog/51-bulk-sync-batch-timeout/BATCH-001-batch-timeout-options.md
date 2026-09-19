# BATCH-001: Decide an approach for the whole-batch sync timeout

**Epic:** Bulk Sync Batch Timeout
**Status:** backlog — needs a decision before scoping
**Depends on:** none
**Estimated size:** unknown until an option below is picked (ranges small→medium-large)

## Goal
Pick and scope a real fix for the gap [STRESS-005](../../planned/49-full-loop-stress-testing/STRESS-005-full-cold-sync-real-scale.md) found: an untargeted `ragctl sync` against a large real project (560+ never-synced dependencies) cannot complete in one invocation, because `internal/daemon/scheduler.go`'s `execute` wraps the *entire* pending plan in one `maxActionDuration = 30 * time.Minute` context, not per-dependency. Once exhausted, every remaining action in the batch fails instantly with a generic `context deadline exceeded`, indistinguishable in the output from hundreds of unrelated real per-dependency failures.

## Relevant existing behavior
`maxActionDuration`'s own code comment is explicit about why it exists: `context.WithoutCancel(s.base)` deliberately lets an in-flight sync survive `Shutdown` rather than leave a half-built generation, but that guarantee needs *some* ceiling or a single hung network call (an unreachable embedder or vector backend) keeps the scheduler goroutine — and the whole daemon process — alive forever. This is a real liveness/hang-protection guarantee, not an incidental bug, and untested at this batch scale rather than wrong by design. `longRunningRequestTimeout = 35 * time.Minute` (`internal/daemon/client/client.go`) is the client-side mirror of the same constraint on the HTTP round-trip.

## Options

**Option A — Raise the ceiling (make it larger and/or configurable).**
Bump `maxActionDuration`/`longRunningRequestTimeout` to something large enough for realistic worst-case batches (e.g. a few hours), possibly config-driven. Lowest-risk, smallest change. Doesn't actually fix the underlying shape — a large enough project (or a slow enough network) still eventually exceeds any fixed ceiling, and the failure mode (a wall of misleading per-dependency `context deadline exceeded` lines) is unchanged when it does.

**Option B — Replace the whole-batch ceiling with a per-action timeout + no-progress watchdog.**
Keep the existing per-operation timeouts (git clone's `defaultCloneTimeout`, Ollama's per-request timeout) as the real bound on any *individual* action, and let the batch loop itself run as long as it keeps making forward progress — a watchdog that resets on every completed action and only fires (aborting the batch, `Shutdown`-safe) after N minutes of *no* action completing, not after a fixed total elapsed time. This is the option that actually lets a real large-scale full sync complete in one invocation while preserving the original hang-protection guarantee (a genuinely stuck daemon still gets caught, just measured by "nothing progressed" rather than "too much wall-clock passed"). Larger, more architecturally sensitive change — touches the daemon's own liveness contract, needs real test coverage for both the happy path (a slow-but-progressing large batch completes) and the protection path (a genuinely hung call still gets killed).

**Option C — Keep the ceiling, but fail the batch cleanly instead of cascading.**
When the shared context is found already `Done()` before starting the next action, stop immediately and report one clear message ("sync time budget exceeded after N of M actions completed; re-run `ragctl sync` to continue with the remaining M-N") instead of letting every subsequent action independently fail with the same generic error. Cheap, no architectural risk, ships regardless of what A/B decide — but doesn't remove the need to manually re-invoke `ragctl sync` repeatedly to make progress on a truly large project, since each re-run only gets its own fresh 30-minute slice.

## Recommendation
**C should ship regardless of A/B** — it's a small, self-contained diagnosability fix with no architectural risk (detect an already-cancelled context before starting the next plan action, stop the loop, report one clear message instead of N misleading ones). **B is the real fix for the capacity problem itself** but changes the daemon's liveness/hang-protection contract, which is a bigger, more sensitive decision than STRESS-001's resolver-only fix — flagged here rather than assumed, unlike that one. **A is the fallback** if B's watchdog design turns out more complex than it's worth for how often real projects actually hit this (most registered projects are nowhere near terraform's scale).

## Non-goals (for whichever option is picked)
- No change to any individual operation's own timeout (git clone, Ollama's HTTP client) — those stay as they are regardless of which option ships.
- No new configuration surface beyond what the chosen option strictly needs (if A is picked, one new config value; if B, no new config at all — the watchdog interval can be a constant like the timeouts it replaces).

## Tests
- Whichever option is picked needs a fast, non-network unit test proving the specific mechanism (e.g. for C: a fake plan + a pre-cancelled context produces exactly one clear "budget exceeded" message, not N cascading ones; for B: a fake slow-but-progressing action sequence completes past the old ceiling, and a fake truly-hung action still gets killed by the watchdog) — STRESS-005 itself is the live-scale confirmation, not something to re-run for every future change here.

## Acceptance criteria
- [ ] An option (A/B/C, or a combination) is chosen and written up as a real, buildable `Design` section.
- [ ] Re-running STRESS-005 against real `hashicorp/terraform` after the fix ships either completes the full batch, or — at minimum — fails with one clear "budget exceeded, re-run to continue" message instead of hundreds of misleading per-dependency `context deadline exceeded` lines.
