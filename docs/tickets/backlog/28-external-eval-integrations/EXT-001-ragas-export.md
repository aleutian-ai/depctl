# EXT-001: Ragas export

**Epic:** External evaluation integrations
**Status:** planned
**Depends on:** EVAL-004
**Estimated size:** small

## Goal
Export eval case results in a format easy for an external Python Ragas runner to consume, without adding any Python runtime dependency to `depctl` itself.

## Non-goals
- No embedded Python execution.
- Not release-blocking for v0.1.

## Simplicity constraints
- This is a single export format (flat JSONL) reusing the data already produced by EVAL-004 (`eval export`); do not build a second eval pipeline.

## Design
Add a `--format ragas` option to the existing `depctl eval export` command (EVAL-004). Output is JSONL, one object per case, with fields Ragas commonly expects:

```json
{"question": "...", "answer": "...", "contexts": ["..."], "ground_truth": "..."}
```

Map from `CaseResult` (EVAL-002) fields: `Query` → `question`, retrieved chunk texts → `contexts`, expected version/required terms description → `ground_truth`, and the best matching retrieved text → `answer` if available (otherwise omit `answer`).

## Inputs / Outputs
- Input: an eval run's stored `CaseResult` data.
- Output: a `.jsonl` file in Ragas-compatible shape.

## Failure behavior
Missing fields (e.g. no retrieved contexts) produce a record with empty arrays rather than failing the export.

## Tests
- Export a fixture eval run and validate the JSONL is well-formed and matches the expected field mapping.

## Acceptance criteria
- [ ] `depctl eval export --format ragas` produces valid JSONL.
- [ ] No Python dependency added to `go.mod` or the build.
- [ ] Documented example in `examples/ragas/` showing how to feed the export into a Ragas script.
