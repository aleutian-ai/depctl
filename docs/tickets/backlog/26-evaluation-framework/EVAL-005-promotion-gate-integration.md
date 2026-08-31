# EVAL-005: Promotion gate integration

**Epic:** Evaluation framework
**Status:** planned
**Depends on:** VAL-004 (atomic promotion), EVAL-002
**Estimated size:** small

## Goal
Allow generation promotion (VAL-004) to be gated on deterministic eval thresholds, so a candidate generation that regresses version correctness cannot become active.

## Non-goals
- Does not add retrieval-metric (EVAL-003) gating in v1 — that suite stays informational only, per the source plan.
- No automatic remediation/retry; a failing candidate simply stays unpromoted.

## Simplicity constraints
- This is config + one extra check inserted into the existing VAL-004 promotion flow. Do not build a generic "policy engine."
- Thresholds are two float config values, not a rules DSL.

## Design
Config addition (`internal/config`):

```yaml
promotion:
  version_correctness_min: 0.99
  stale_retrieval_max: 0.01
```

In the promotion flow (VAL-004), after structural/sanity/smoke validation passes and before the bbolt write transaction, if `promotion.version_correctness_min`/`stale_retrieval_max` are configured, run EVAL-002 against the candidate generation's own eval cases (if any are scoped to it) and compare against thresholds. On failure, the generation stays in `VALIDATING`/`FAILED` state rather than proceeding to `READY`/`ACTIVE`.

If no eval cases apply to the candidate's dependency, skip the gate (do not fail promotion for lack of data) and log this explicitly.

## Inputs / Outputs
- Input: candidate generation, configured thresholds, applicable eval cases.
- Output: promotion proceeds or is blocked with a clear reason recorded on the generation record.

## Failure behavior
A gate failure is a normal, expected outcome (not a crash) — it must produce a human-readable reason string stored alongside the generation's `FAILED` state.

## Tests
- Candidate passing thresholds promotes normally.
- Candidate below `version_correctness_min` is blocked; generation remains in prior state.
- No applicable eval cases → promotion proceeds (gate skipped, logged).

## Acceptance criteria
- [ ] Config fields added and validated.
- [ ] Promotion flow blocks on threshold failure with a stored reason.
- [ ] Skip-when-no-cases behavior is explicit and tested.
