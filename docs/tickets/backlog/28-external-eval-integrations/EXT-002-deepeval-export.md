# EXT-002: DeepEval export

**Epic:** External evaluation integrations
**Status:** planned
**Depends on:** EVAL-004
**Estimated size:** small

## Goal
Provide a JSONL export format and usage examples compatible with DeepEval, mirroring EXT-001's approach.

## Non-goals
- No embedded Python execution or DeepEval SDK vendoring.
- Not release-blocking for v0.1.

## Simplicity constraints
- Reuse the same `eval export` command and underlying data as EXT-001; add one more `--format deepeval` option rather than a parallel export pipeline.

## Design
`ragctl eval export --format deepeval` writes JSONL with DeepEval's expected test-case shape:

```json
{"input": "...", "actual_output": "...", "retrieval_context": ["..."], "expected_output": "..."}
```

Field mapping is analogous to EXT-001 (`Query` → `input`, retrieved texts → `retrieval_context`, etc.).

## Inputs / Outputs
- Input: stored eval run results.
- Output: `.jsonl` file in DeepEval-compatible shape.

## Failure behavior
Same as EXT-001: missing data yields empty fields, not a failed export.

## Tests
- Export a fixture run and validate JSONL shape against DeepEval's documented field names.

## Acceptance criteria
- [ ] `--format deepeval` implemented.
- [ ] Example under `examples/deepeval/` showing a minimal DeepEval test file consuming the export.
