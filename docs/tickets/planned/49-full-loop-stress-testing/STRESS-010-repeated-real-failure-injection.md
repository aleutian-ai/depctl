# STRESS-010: Repeated real failure injection during sync

**Epic:** Full-Loop Stress Testing
**Status:** done — 2026-09-29
**Depends on:** FIX-001 (Generation.State=FAILED on Replicate failure)
**Estimated size:** medium

## Goal
Inject real (not fake-embedder-simulated) sync failures repeatedly against a real backend — a bad registry ref, a genuinely unreachable embedder, a vector backend temporarily taken down mid-sync — and confirm FIX-001's `Generation.State = FAILED` transition holds for each real failure mode, not just the one synthetic case (`fakeEmbedder.failAfter`) the unit tests inject. This is the real-infrastructure counterpart to VALID-001/FIX-001's fixture-scale tests.

## Non-goals
- No new failure-handling logic — this verifies existing behavior under real failure conditions, it doesn't add new ones unless a real gap is found.

## Simplicity constraints
- Three real failure modes, not an exhaustive matrix: (1) a manifest for `github.com/spf13/cobra` with `ref: v99.99.99` (a real repo, a `ref` that doesn't exist), (2) Ollama stopped/unreachable during a sync of `github.com/google/uuid` (tiny, fast, so the failure window is easy to time), (3) Qdrant stopped/unreachable during a sync of the same dependency (mid-Replicate, if timing allows).

## Design
1. **Bad ref**: point a registry manifest at `github.com/spf13/cobra` with `ref: v99.99.99` (doesn't exist); `ragctl sync --dependency github.com/spf13/cobra`; confirm `ErrAcquisition`-wrapped failure, `Generation.State == FAILED`.
2. **Embedder down**: stop the local Ollama process, `ragctl sync --dependency github.com/google/uuid` (never-synced); confirm a clear, typed failure (not a hang), `Generation.State == FAILED` once `Replicate` reaches the embed step. Restart Ollama afterward.
3. **Vector backend down**: stop the `ragctl-qdrant` container mid-sync of `github.com/google/uuid` (timed to land during `Replicate`'s upsert step, similar timing approach to STRESS-006); confirm the same `FAILED` transition. Restart the container afterward.
4. For each: `ragctl gc --orphans --dry-run` afterward — confirm the failed generation is correctly detected as an orphan candidate.
5. For each: re-run `sync` for the same dependency after fixing the underlying issue — confirm it completes normally.

## Inputs / Outputs
- Input: three real failure conditions, injected against a real sync in progress.
- Output: pass/fail on `Generation.State == FAILED` for each real failure mode, correct orphan-GC detection, and clean recovery on retry.

## Failure behavior
- Any failure mode that leaves `Generation.State` at something other than `FAILED` (contradicting FIX-001's fixture-scale guarantee) is this ticket's finding — worth its own follow-up if the real-world failure shape differs from what the fake-embedder test injects.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [x] All three real failure modes (bad ref, embedder down, vector backend down) produce `Generation.State == FAILED`. All three: real, clean, typed errors — never a hang, never silent.
- [x] Each is correctly detected by `ragctl gc --orphans --dry-run` afterward, and a real `gc --orphans` run cleans up all three correctly.
- [x] Each recovers, but not via a *plain* retry as originally assumed — see notes. Every one required `ragctl sync --rebuild --dependency <name>` (OPS-004), not a plain re-sync, and `ragctl doctor`'s `checkReferencedButNeverBuilt` (OPS-005) correctly flagged all three as stuck in between.

## Post-implementation note (2026-09-29)

Live-verified against a real daemon, real (isolated, safely-killable) Ollama and Qdrant instances — never the shared production ones — and a real fixture (`github.com/spf13/cobra`, `github.com/google/uuid`, `golang.org/x/sys`).

**All three real failure modes reproduced cleanly, exactly as designed:**
1. **Bad ref**: a real user-tier registry manifest overriding cobra's fallback with `ref: v99.99.99` — real error: `acquisition failed: ... git rev-parse v99.99.99: fatal: ambiguous argument ... unknown revision`.
2. **Embedder down**: a second, isolated `ollama serve` instance (a different port, never the real shared Ollama app) killed mid-sync of `uuid` — real error: `embed batch: ... dial tcp 127.0.0.1:11435: connect: connection refused`.
3. **Vector backend down**: the isolated Podman Qdrant container stopped mid-embedding of `golang.org/x/sys` (confirmed actively building via `ragctl status` polling before stopping it) — real error: `upsert batch: qdrant Upsert: ... connection refused`.

All three: `Generation.State == FAILED`, confirmed via `ragctl gc --orphans --dry-run` naming each one correctly, and a real `gc --orphans` run deleted all three cleanly afterward with zero failures.

**The recovery finding, not a new bug — this generalizes STRESS-006's own OPS-004/OPS-005 finding to a whole new trigger class.** A *plain* re-sync after fixing the underlying issue (removing the bad manifest, restarting the isolated Ollama/Qdrant) **NOOPs every time** — `0 synced, 0 failed, 0 skipped` — for all three failure modes, confirmed. This isn't specific to a `kill -9` interruption (STRESS-006's own trigger): `internal/planner`'s `Plan` NOOPs unconditionally once a dependency's reference exists at its current version, regardless of whether the matching generation ever succeeded, was killed mid-flight, or **cleanly failed** with a real, typed error. `ragctl doctor`'s `checkReferencedButNeverBuilt` (OPS-005, already shipped the day before this ticket ran) correctly flagged all three stuck dependencies by name with the exact `--rebuild --dependency <name>` remedy — confirmed working exactly as designed, not just for STRESS-006's narrower kill-interruption case. `ragctl sync --rebuild --dependency <name>` (OPS-004) recovered all three for real. No new ticket needed — OPS-004/OPS-005 already cover this generalized case correctly; this ticket's value was confirming that generalization live, not finding a new gap.

Real production Ollama and Qdrant collection (`ragctl`, 1,664,155 points) confirmed healthy and completely untouched throughout — every kill/stop targeted an isolated, verified-by-path/port instance, never the shared ones.
