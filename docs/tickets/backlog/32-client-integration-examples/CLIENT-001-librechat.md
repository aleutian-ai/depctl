# CLIENT-001: LibreChat integration example

**Epic:** Client integration examples
**Status:** planned
**Depends on:** MCP-003 (MCP tools)
**Estimated size:** small

## Goal
Provide an example MCP configuration for connecting LibreChat to a running `ragctl serve` instance.

## Non-goals
- No LibreChat-specific code in `ragctl` core — configuration/documentation only.

## Simplicity constraints
- A single example config file plus a short README; no custom LibreChat plugin or bespoke integration layer.

## Design
Directory: `examples/librechat/`

```text
examples/librechat/
  librechat.yaml   # MCP server entry pointing at `ragctl serve`'s stdio/HTTP MCP endpoint
  README.md        # setup steps: run `ragctl serve`, add config, restart LibreChat
```

Config should show both stdio-launch and HTTP-endpoint MCP registration forms if LibreChat supports both, matching whatever transport MCP-001 selected.

## Inputs / Outputs
- Input: none (documentation).
- Output: `examples/librechat/` directory.

## Failure behavior
N/A.

## Tests
N/A — manually verified against a real LibreChat instance before merging; not part of the Go test suite.

## Acceptance criteria
- [ ] Example config matches the actual MCP transport `ragctl serve` exposes.
- [ ] README walks through setup end-to-end.
