# EVAL-004: `ragctl eval` command group

**Epic:** Evaluation framework
**Status:** planned
**Depends on:** EVAL-002, EVAL-003
**Estimated size:** small

## Goal
Expose the evaluation framework via CLI subcommands so users/CI can run and inspect eval results without writing Go code.

## Non-goals
- No web dashboard.
- No historical trend storage/database — results are ephemeral per run unless exported (see `eval export`).

## Simplicity constraints
- Reuse Cobra (already a dependency for the main CLI). Do not introduce a second CLI framework or a separate binary.
- `eval show`/`eval list` operate on the most recent run's output file(s) on disk; do not build a persistent eval-run database in bbolt for v1.

## Design
Package: `internal/cli` (or wherever other Cobra commands live), subcommand group `eval`.

```bash
ragctl eval run [--cases <dir>] [--json]
ragctl eval list
ragctl eval show <run-id>
ragctl eval export <run-id> --format json|csv
```

- `eval run` loads cases (EVAL-001), executes EVAL-002 (always) and EVAL-003 (if labeled cases present), writes a result file to `~/.local/share/ragctl/eval/<run-id>.json`, and prints a human-readable summary (or JSON with `--json`).
- `eval list` lists result files in that directory.
- `eval show`/`eval export` read a specific result file.

## Inputs / Outputs
- Input: case directory, existing generation/backend state via the query service.
- Output: result files on disk, console summary.

## Failure behavior
Non-zero exit code if `VersionCorrectness` or `StaleRetrievalRate` cross unset warning thresholds is NOT this ticket's job (that's EVAL-005 for the promotion gate); `eval run` itself only fails on infrastructure errors (can't reach storage/backend).

## Tests
- `eval run` against a fixture case set produces a result file with expected metric values.
- `eval show` on a missing run-id returns a clear error.

## Acceptance criteria
- [ ] All four subcommands implemented per the CLI shape above.
- [ ] Result files are valid JSON matching `Metrics`/`RetrievalMetrics`.
- [ ] Integration test runs `eval run` end-to-end against fixture data.
