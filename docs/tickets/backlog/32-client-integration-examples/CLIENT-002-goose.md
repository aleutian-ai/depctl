# CLIENT-002: Goose integration example

**Epic:** Client integration examples
**Status:** planned
**Depends on:** MCP-003 (MCP tools)
**Estimated size:** small

## Goal
Provide an example extension/MCP configuration for connecting the Goose agent runtime to `depctl serve`.

## Non-goals
- No Goose-specific code in `depctl` core — configuration/documentation only.

## Simplicity constraints
- A single example config file plus a short README, mirroring CLIENT-001's structure for consistency.

## Design
Directory: `examples/goose/`

```text
examples/goose/
  goose-mcp.yaml   # or whatever config format Goose's MCP extension mechanism expects
  README.md        # setup steps
```

Document the intended division of responsibility explicitly in the README: `depctl` supplies versioned external knowledge; Goose remains responsible for agent execution and other tools (filesystem, shell, etc.).

## Inputs / Outputs
- Input: none (documentation).
- Output: `examples/goose/` directory.

## Failure behavior
N/A.

## Tests
N/A — manually verified against a real Goose instance before merging.

## Acceptance criteria
- [ ] Example config matches Goose's current MCP extension configuration format.
- [ ] README documents the depctl/Goose responsibility split.
