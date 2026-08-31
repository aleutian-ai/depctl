# EVAL-001: Eval case model

**Epic:** Evaluation framework
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
Define a Go struct representing a single deterministic evaluation case: a query against a known project/dependency with an expected answer shape, so correctness can be checked without an LLM judge.

## Non-goals
- No eval runner, no scoring, no CLI (see EVAL-002/EVAL-004).
- No support for free-text/LLM-graded cases in v1.

## Simplicity constraints
- A plain struct + JSON/YAML (de)serialization is enough. Do not build a generic "test case DSL" or plugin system for case types.
- Do not add fields speculatively (e.g. tags, owners, priority) beyond what EVAL-002/EVAL-003 will consume.

## Design
Package: `internal/eval`

```go
type EvalCase struct {
    ID                string   `json:"id" yaml:"id"`
    ProjectID         string   `json:"project_id" yaml:"project_id"`
    Dependency        string   `json:"dependency" yaml:"dependency"`
    Query             string   `json:"query" yaml:"query"`
    ExpectedVersion   string   `json:"expected_version" yaml:"expected_version"`
    RequiredTerms     []string `json:"required_terms" yaml:"required_terms"`
    ForbiddenVersions []string `json:"forbidden_versions" yaml:"forbidden_versions"`
    MinResults        int      `json:"min_results" yaml:"min_results"`
}
```

Cases are loaded from YAML files under `testdata/eval/` (or a configured directory). Provide `LoadCases(dir string) ([]EvalCase, error)` that reads all `*.yaml` files in a directory, non-recursive.

## Inputs / Outputs
- Input: YAML files on disk.
- Output: `[]EvalCase` in memory, validated (non-empty ID/ProjectID/Dependency/Query).

## Failure behavior
`LoadCases` returns a wrapped error naming the offending file on parse/validation failure; it does not skip bad files silently.

## Tests
- Valid case file loads correctly.
- Missing required field (e.g. empty Query) is rejected.
- Duplicate IDs across files are rejected.

## Acceptance criteria
- [ ] `EvalCase` struct matches the fields above.
- [ ] `LoadCases` loads and validates a directory of YAML fixtures.
- [ ] Unit tests cover valid and invalid fixtures.
