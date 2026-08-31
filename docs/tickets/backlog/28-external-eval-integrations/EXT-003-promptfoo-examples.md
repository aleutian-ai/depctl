# EXT-003: Promptfoo examples

**Epic:** External evaluation integrations
**Status:** planned
**Depends on:** MCP-003 (MCP tools available for a client to call)
**Estimated size:** small

## Goal
Provide example Promptfoo configuration under `examples/promptfoo/` covering security/correctness-relevant scenarios for agents using `ragctl` via MCP.

## Non-goals
- No Go code changes — this is documentation/examples only.
- Not release-blocking for v0.1.

## Simplicity constraints
- Ship a small number of illustrative cases, not a comprehensive benchmark suite. Three cases as named in the source plan are sufficient for v1.

## Design
Directory: `examples/promptfoo/`

Provide a `promptfooconfig.yaml` and prompt fixtures exercising:

```text
1. wrong-version retrieval — agent should surface the project's exact pinned version, not latest.
2. malicious retrieved instructions — retrieved content includes an injected instruction; agent should treat it as data, not follow it (see SEC-002 labeling).
3. stale docs regression — verify a superseded generation's content does not leak into a query scoped to the active generation.
```

Each case configures Promptfoo to call the `ragctl` MCP server (per CLIENT-003's generic MCP client documentation) and assert on the response.

## Inputs / Outputs
- Input: a running `ragctl serve` instance with fixture data loaded.
- Output: Promptfoo pass/fail report.

## Failure behavior
N/A (example/documentation ticket) — assertions failing is the expected signal Promptfoo surfaces to the user running the example.

## Tests
- Manually run `promptfoo eval` against the example config in CI or as a documented manual step; not required to be wired into the main Go test suite.

## Acceptance criteria
- [ ] `examples/promptfoo/promptfooconfig.yaml` covers the three named scenarios.
- [ ] README in the examples directory explains prerequisites (a running `ragctl serve` with fixture data).
