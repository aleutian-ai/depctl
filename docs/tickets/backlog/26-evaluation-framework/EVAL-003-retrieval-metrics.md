# EVAL-003: Retrieval quality metrics

**Epic:** Evaluation framework
**Status:** planned
**Depends on:** EVAL-001, MCP-002
**Estimated size:** medium

## Goal
Support labeled benchmark cases (query + ranked list of relevant result IDs) and compute standard retrieval metrics: Precision@K, Recall@K, MRR, nDCG.

## Non-goals
- Not part of the promotion gate (deterministic version correctness in EVAL-002 is what gates promotion; this suite is informational).
- No UI/visualization of results.

## Simplicity constraints
- Implement the four metrics as small pure functions over `(retrieved []string, relevant []string)`. Do not pull in an external IR-metrics library for four formulas.
- K is a single configurable constant per run (e.g. 10), not a sweep over multiple K values in v1.

## Design
Package: `internal/eval`

```go
type LabeledCase struct {
    ID       string
    Query    string
    Relevant []string // ranked or unranked relevant result IDs
}

type RetrievalMetrics struct {
    PrecisionAtK float64
    RecallAtK    float64
    MRR          float64
    NDCG         float64
}

func RunRetrieval(ctx context.Context, q QueryService, cases []LabeledCase, k int) (RetrievalMetrics, error)
```

## Inputs / Outputs
- Input: labeled cases (loaded via a YAML format similar to EVAL-001), `QueryService`, K.
- Output: aggregate `RetrievalMetrics` (mean over cases).

## Failure behavior
Same as EVAL-002: a single case query failure is recorded as zero-relevance for that case rather than aborting the run.

## Tests
- Synthetic case with known ranked relevant/retrieved lists — verify each metric against hand-computed expected values.
- K larger than result count does not panic.

## Acceptance criteria
- [ ] All four metrics implemented and unit-tested against hand-computed fixtures.
- [ ] `RunRetrieval` aggregates cleanly across a case set.
- [ ] This suite is explicitly documented as optional for promotion gating (per EVAL-005).
