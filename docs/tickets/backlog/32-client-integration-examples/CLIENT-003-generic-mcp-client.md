# CLIENT-003: Generic MCP client documentation

**Epic:** Client integration examples
**Status:** planned
**Depends on:** MCP-003 (MCP tools)
**Estimated size:** small

## Goal
Document how to configure any generic MCP client to connect to `depctl serve`, independent of vendor-specific tooling. This is explicitly called out in the source plan as more important than the vendor-specific tutorials (CLIENT-001/CLIENT-002).

## Non-goals
- No code changes — documentation only.

## Simplicity constraints
- One doc page. Do not attempt to enumerate every possible MCP client; cover the transport/connection details generically (stdio command invocation, or HTTP endpoint + auth if applicable) so any compliant client can be wired up by analogy.

## Design
File: `docs/mcp-integration.md`

Cover:
- how to start `depctl serve` and what MCP transport(s) it exposes (per MCP-001's decision),
- the full tool list and a one-line description of each (`search_dependency_docs`, `get_dependency_version`, `list_project_dependencies`, `get_release_changes`, `knowledge_status`, and `sync_project` noting it may be disabled by default for read-only clients),
- example resource URIs (`depctl://project/{id}/dependencies`, `depctl://dependency/{ecosystem}/{name}/{version}`, `depctl://generation/{id}`),
- a minimal generic client config snippet (e.g. raw JSON MCP server registration) that CLIENT-001/CLIENT-002 can both link back to.

## Inputs / Outputs
- Input: none (documentation).
- Output: `docs/mcp-integration.md`.

## Failure behavior
N/A.

## Tests
N/A — documentation ticket; verify tool/resource list stays in sync with MCP-003's actual implementation at review time.

## Acceptance criteria
- [ ] Doc covers transport, full tool list, and resource URI scheme.
- [ ] CLIENT-001 and CLIENT-002 examples link to this doc rather than duplicating the tool list.
