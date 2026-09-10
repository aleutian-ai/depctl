# VAL-003: Version correctness smoke test

**Epic:** Validation and Promotion
**Status:** done
**Depends on:** VEC-002
**Estimated size:** small

## Goal
For a candidate generation, query several known terms drawn from its own indexed content against the vector backend and confirm every returned result's metadata matches the candidate's version and generation ID — a deterministic proof the replica isn't cross-contaminated with another version's data.

## Non-goals
- No semantic relevance scoring — this only checks metadata correctness of returned results, not answer quality (that's the evaluation framework, milestone 25).

## Simplicity constraints
- Sample a small fixed number of chunks (e.g. 5) from the candidate generation itself to use as queries — no separate curated query fixture set needed for this smoke test.

## Design
- Package: `internal/lifecycle/validate`, function:
```go
func VersionCorrectness(ctx context.Context, backend backend.VectorBackend, gen domain.Generation, sampleChunks []domain.Chunk) (StructuralResult, error)
```
- For each sample chunk: embed its text (reuse the configured embedder), query the backend filtered to `generation = gen.ID`, and assert every returned point's `version` metadata equals `gen.DependencyVersion.Version` and `generation` metadata equals `gen.ID`.
- Sample selection: deterministic (e.g. first N chunks by sorted chunk ID) so results are reproducible in tests.

## Inputs / Outputs
- Input: candidate generation, its chunks, live vector backend.
- Output: pass/fail with reasons (e.g. "chunk X query returned point from version Y").

## Failure behavior
- Any mismatch fails the whole check (all mismatches collected, not fail-fast) — this is a release-blocking correctness gate.

## Tests
- Fixture: two generations of the same dependency (different versions) both indexed; querying candidate's own terms returns only candidate's version.
- Corrupt fixture (manually insert a wrong-version point) → check fails and reports it.

## Acceptance criteria
- [x] Smoke test is deterministic (same generation → same result every run).
- [x] Test proves cross-version isolation using two real indexed versions of one dependency.
