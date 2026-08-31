# EVAL-002: Deterministic version correctness metrics

**Epic:** Evaluation framework
**Status:** planned
**Depends on:** EVAL-001, MCP-002 (knowledge query service)
**Estimated size:** medium

## Goal
Run the loaded `EvalCase` set against the knowledge query service and compute deterministic, LLM-free correctness metrics.

## Non-goals
- No retrieval-quality metrics (Precision@K, Recall@K, MRR, nDCG) — that is EVAL-003.
- No promotion-gate wiring — that is EVAL-005.

## Simplicity constraints
- Compute metrics as simple ratios over the case set; do not build a generic metrics/statistics framework.
- Formulas must be documented as comments next to the code — do not scatter definitions across docs only.

## Design
Package: `internal/eval`

```go
type Metrics struct {
    VersionCorrectness   float64 // fraction of cases where returned version == ExpectedVersion
    StaleRetrievalRate    float64 // fraction of cases returning a ForbiddenVersions match
    ProvenanceCompleteness float64 // fraction of results with non-empty source URI/authority
    AuthorityCompliance    float64 // fraction of results with authority >= case's minimum (if any)
}

func Run(ctx context.Context, q QueryService, cases []EvalCase) (Metrics, []CaseResult, error)
```

`QueryService` is the existing interface from MCP-002 (`SearchKnowledge`, etc.) — reuse it directly, do not redefine a parallel interface.

Each `CaseResult` records the case ID, actual returned version(s), pass/fail per metric, for later reporting (EVAL-004).

## Inputs / Outputs
- Input: `[]EvalCase`, a `QueryService`.
- Output: aggregate `Metrics` plus per-case `CaseResult` list.

## Failure behavior
A query error for one case is recorded as a failure for that case (does not abort the run); the overall `Run` only returns an error if the query service itself is unreachable for every case.

## Tests
- Fixture with 3 cases: one correct-version, one stale-version, one missing-provenance — verify each metric computes correctly.
- Query service returning zero results for a case with `MinResults > 0` is scored as a failure.

## Acceptance criteria
- [ ] Metrics formulas match the definitions above and are documented in code.
- [ ] `Run` produces both aggregate and per-case results.
- [ ] Unit tests validate each metric independently.
