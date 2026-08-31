# LOCAL-002: Local query fallback

**Epic:** Local-only retrieval mode
**Status:** planned
**Depends on:** LOCAL-001, MCP-002 (knowledge query service)
**Estimated size:** small

## Goal
When no vector backend is configured, make the MCP/query service automatically use the Bleve lexical backend (LOCAL-001) so `ragctl` works immediately after install with zero external infrastructure.

## Non-goals
- No automatic migration of existing vector-backend data into Bleve, or vice versa.
- Does not change the recommendation that a real vector backend gives better semantic retrieval — this is purely a no-config default path.

## Simplicity constraints
- This is a config-resolution default, not a runtime failover/circuit-breaker between backends. If a vector backend is configured, use it; if not, use Bleve. No automatic switching mid-run.

## Design
In config loading (`internal/config`), if `vector.backend` is unset/empty, default it to `local` (the Bleve adapter from LOCAL-001) at validation time, and log at `init`/`serve` startup: `"no vector backend configured — using local Bleve lexical search; configure vector.backend for semantic search"`.

The knowledge query service (MCP-002) is backend-agnostic already (it depends on the `VectorBackend` interface), so no query-service code changes should be needed beyond ensuring the resolved backend instance is whichever one config resolution picked.

## Inputs / Outputs
- Input: config with `vector.backend` unset.
- Output: daemon runs using the local Bleve backend transparently.

## Failure behavior
N/A beyond LOCAL-001's own failure behavior — this ticket only affects backend selection.

## Tests
- Config with no `vector.backend` set resolves to the local backend.
- `ragctl init` + `ragctl sync` + `ragctl serve` with zero external services succeeds end-to-end using fixture data (this is the "works immediately after install" proof).

## Acceptance criteria
- [ ] Missing `vector.backend` config defaults to local Bleve.
- [ ] Startup log clearly states the fallback is active.
- [ ] End-to-end test: init → sync → serve → query, no external backend running.
