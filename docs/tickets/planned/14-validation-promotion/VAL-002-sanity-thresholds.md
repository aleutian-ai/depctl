# VAL-002: Sanity thresholds

**Epic:** Validation and Promotion
**Status:** planned
**Depends on:** VAL-001
**Estimated size:** small

## Goal
Compare a candidate generation's object/chunk counts and parser error rate against the prior active generation for the same dependency, and block promotion if the candidate looks implausible (e.g. object count collapses from 18,000 to 220 without genuine cause).

## Non-goals
- No automatic "this is fine, source genuinely shrank" override heuristics — v0.1 just blocks and requires `--force` (see PLAN-003) to override.

## Simplicity constraints
- Three configurable thresholds only (object count floor ratio, chunk count ceiling ratio, parser error rate ceiling) — do not build a generic statistical anomaly detector.

## Design
- Package: `internal/lifecycle/validate` (same package as VAL-001), function `Sanity(ctx context.Context, candidate, prior domain.Generation, candidateManifest, priorManifest data.Manifest, cfg SanityConfig) (StructuralResult, error)` — same result shape as VAL-001 for uniform handling.
- Config:
```yaml
promotion:
  min_object_count_ratio: 0.5   # candidate must have >= 50% of prior object count
  max_chunk_count_ratio: 3.0    # candidate must have <= 300% of prior chunk count
  max_parser_error_rate: 0.05   # <=5% of source files may fail to parse
```
- If there is no prior generation (first sync for this dependency), sanity checks are skipped (nothing to compare against).

## Inputs / Outputs
- Input: candidate + prior generation manifests, threshold config.
- Output: pass/fail with reasons, same as VAL-001.

## Failure behavior
- Missing/zero prior object count is treated as "no prior to compare," not a divide-by-zero error.

## Tests
- Candidate with object count below floor ratio → fails.
- Candidate with chunk count above ceiling ratio → fails.
- First-ever generation for a dependency (no prior) → sanity check auto-passes.
- Thresholds configurable via config and respected in the check.

## Acceptance criteria
- [ ] Guardrails are configurable, not hardcoded.
- [ ] A generation with an implausible count drop is blocked from promotion.
