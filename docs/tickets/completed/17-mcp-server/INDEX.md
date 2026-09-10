# Epic: MCP Server

Exposes ragctl's version-aware knowledge to AI coding agents via the Model Context Protocol — the primary agent-facing interface. Corresponds to Milestone 16 in the implementation plan.

## Tickets
- [MCP-001](MCP-001-select-go-mcp-sdk.md) — Choose and record a maintained Go MCP SDK (stdio + streamable HTTP).
- [MCP-002](MCP-002-knowledge-query-service.md) — Transport-agnostic query service: project-aware version resolution + search.
- [MCP-003](MCP-003-mcp-tools.md) — Wire the query service into MCP tools (`search_dependency_docs`, etc.), served by `ragctl serve`.
- [MCP-004](MCP-004-offline-query-test.md) — Release-blocking test proving offline, version-correct MCP queries work with no network.
