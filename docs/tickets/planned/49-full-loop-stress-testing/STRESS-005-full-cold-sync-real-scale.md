# STRESS-005: Full cold sync at real scale

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** STRESS-001 (reuses its project — `hashicorp/terraform`, or `prometheus/prometheus` if that was the fallback used)
**Estimated size:** medium

## Goal
Run a full, cold `ragctl sync` (no `--dependency` filter — every resolved dependency) against STRESS-001's 100+-dependency project, against a real embedder (Ollama) and real vector backend (Qdrant), and measure real wall-clock and disk usage. This is the actual scenario that originally motivated GIT-005 (14+ minutes, 828MB, zero completions, before that fix) — this ticket re-measures it post-fix, at the same real scale, not a fixture-sized proxy.

## Non-goals
- No comparison against Grounded Docs or any other system in this ticket — that's `context-evals` scope per epic 45's own note.
- No fixing of anything found beyond confirming/denying the GIT-005 improvement — a new bottleneck found here gets its own follow-up ticket.

## Simplicity constraints
- Reuses the already-running `ragctl-qdrant` container and local Ollama from this session's existing live-testing setup (`vector.managed: false` pointed at the existing container) rather than provisioning new infrastructure.

## Design
1. Fresh `ragctl init` (isolated `HOME`), `vector.managed: false` pointed at the real running Qdrant, real Ollama embedder configured.
2. `ragctl scan <STRESS-001 project>`.
3. `time ragctl sync` (no filter) — full cold sync.
4. Record: total wall-clock, `du -sh` of the git cache directory and the Badger/bbolt data directory afterward, `synced`/`failed`/`skipped` counts from the sync report.
5. Compare against the original pre-GIT-005 numbers (14+ min, 828MB, zero completions) qualitatively — this ticket's job is to establish the current real number, not to hit a specific target.

## Inputs / Outputs
- Input: STRESS-001's scanned project, a real embedder and vector backend.
- Output: recorded wall-clock, disk usage, and synced/failed/skipped counts — a real, dated data point (like VALID-003's benchmark), not a one-time anecdote.

## Failure behavior
- Network/Ollama/Qdrant-dependent — skip (not fail) if any of those aren't reachable.
- A failed-dependency count greater than a small, explicable fraction (e.g. a genuinely bad manifest match) is this ticket's finding — record which dependencies failed and why.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note, mirroring VALID-003's dated-measurement format.

## Acceptance criteria
- [ ] A full cold sync of a real 100+-dependency project completes, with wall-clock and disk usage recorded.
- [ ] The recorded numbers are compared against the pre-GIT-005 baseline in this ticket's own write-up.
- [ ] Any failed dependencies are explained, not silently ignored.
