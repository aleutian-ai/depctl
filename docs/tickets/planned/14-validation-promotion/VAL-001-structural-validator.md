# VAL-001: Structural validator

**Epic:** Validation and Promotion
**Status:** planned
**Depends on:** GEN-002, VEC-003
**Estimated size:** small

## Goal
Run a set of deterministic structural checks against a candidate generation before it can be promoted: non-zero counts, backend point count match, manifest completeness, valid version metadata.

## Non-goals
- No LLM-based or semantic quality checks — this is purely structural/deterministic (the lifecycle engine must be testable without an LLM per the plan's development principles).

## Simplicity constraints
- Implement as one function returning a list of check failures — do not build a generic rules-engine/plugin system for validators; there is one validator in v0.1 (this one) plus VAL-002/VAL-003, called directly in sequence.

## Design
- Package: `internal/lifecycle/validate`.
```go
type StructuralResult struct {
    Passed bool
    Failures []string
}
func Structural(ctx context.Context, gen domain.Generation, manifest data.Manifest, replica control.BackendReplica) (StructuralResult, error)
```
- Checks:
  - `manifest.SourceCount > 0`
  - `manifest.ObjectCount > 0`
  - `manifest.ChunkCount > 0`
  - `replica.PointCount == manifest.ChunkCount` (backend point count matches expected chunk count)
  - `manifest.Hash != ""` (manifest hash exists)
  - Every chunk's version metadata equals `gen.DependencyVersion.Version` (no invalid version metadata)

## Inputs / Outputs
- Input: a generation in state `INDEXING`/`VALIDATING` with its manifest and backend replica record.
- Output: pass/fail with a list of human-readable failure reasons, consumed by the promotion flow (VAL-004).

## Failure behavior
- Any failed check keeps the generation in `VALIDATING`/moves it to `FAILED`; failures are collected (not fail-fast on first check) so a single validation run reports everything wrong at once.

## Tests
- Fixture generation with zero chunks → fails "chunk count > 0" check.
- Fixture with `replica.PointCount != manifest.ChunkCount` → fails point-count check.
- Fully valid fixture → passes with empty failure list.

## Acceptance criteria
- [ ] All six structural checks implemented and independently unit-testable.
- [ ] Failing generation never proceeds to promotion (enforced by VAL-004 checking `Passed`).
