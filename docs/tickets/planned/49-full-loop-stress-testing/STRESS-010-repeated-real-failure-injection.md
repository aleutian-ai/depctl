# STRESS-010: Repeated real failure injection during sync

**Epic:** Full-Loop Stress Testing
**Status:** planned
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
- [ ] All three real failure modes (bad ref, embedder down, vector backend down) produce `Generation.State == FAILED`.
- [ ] Each is correctly detected by `ragctl gc --orphans --dry-run` afterward.
- [ ] Each recovers cleanly on retry once the underlying issue is fixed.
