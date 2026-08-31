# ADR-006: MCP as the primary agent interface; official Go SDK

**Status:** Accepted
**Date:** 2026-08-31

## Context

Epics 1–16 built ragctl's whole knowledge lifecycle — resolve, acquire, normalize, chunk, embed, replicate, validate, promote, retain/GC — driven end to end by `ragctl scan`/`sync`/`gc`. None of it is reachable by an external process yet. The Model Context Protocol (MCP) is ragctl's stated agent-facing interface (`ragctl serve`, already a registered CLI stub); this epic is where that stub becomes real.

Building a custom MCP protocol implementation (message framing, JSON-RPC dispatch, capability negotiation, stdio/HTTP transport handling) is exactly the kind of protocol-plumbing this project has no reason to own — CLAUDE.md's "pull in only what the current command needs" cuts the other way here: a maintained SDK is *less* code to own, not more.

Options considered:
- **`github.com/modelcontextprotocol/go-sdk`** — the official Go SDK for MCP servers and clients, maintained in collaboration with Google. Supports both stdio and Streamable HTTP transports on both the server and client side, plus an in-memory transport pair (`NewInMemoryTransports`) built specifically for testing a server and client against each other without a subprocess or real socket — exactly what MCP-004's offline test needs. Spec-complete for MCP 2025-11-25. Actively released (v1.7.0 at the time of this decision).
- **`github.com/mark3labs/mcp-go`** — an earlier, widely-used community SDK. Was a reasonable choice before the official SDK existed, but there's no reason to prefer a community implementation over the official one now that it exists and is actively maintained by the protocol's own stewards (with Google).
- **Hand-rolled implementation** — rejected per MCP-001's own non-goals; only justified if no viable SDK existed.

## Decision

Use **`github.com/modelcontextprotocol/go-sdk`** (package `github.com/modelcontextprotocol/go-sdk/mcp`), the official Go SDK.

Package boundary: `internal/mcp` is the only package that imports `github.com/modelcontextprotocol/go-sdk/mcp` or any other MCP/protocol type. `internal/query` (MCP-002, the business logic every tool calls into) and every domain/storage package remain completely unaware that MCP exists — `internal/mcp` is a thin adapter layer over `internal/query`, per MCP-003's own simplicity constraint ("no business logic in `internal/mcp`").

Tool registration uses the SDK's generic, typed `mcp.AddTool[In, Out]` API (input/output as plain Go structs with `json`/`jsonschema` tags, schema and validation handled by the SDK) rather than the raw untyped `Server.AddTool` — matches this codebase's general preference for typed data over `map[string]any` plumbing wherever the option exists.

## Consequences

**Positive:**
- No protocol/transport code to maintain — message framing, capability negotiation, and both transports come from the SDK.
- `NewInMemoryTransports` makes MCP-004's release-blocking offline test fast and in-process (no subprocess, no real stdio pipe, no port binding) while still exercising the real protocol serialization path between a real client and a real server.
- Typed tool I/O means a malformed agent request fails schema validation before ever reaching `internal/query`, not partway through business logic.

**Negative / tradeoffs:**
- Tied to the upstream SDK's release cadence and any breaking changes in its (still relatively young, v1.x) API — mitigated by the strict `internal/mcp` boundary: an SDK-breaking change is contained to one package, never leaks into `internal/query` or beyond.

## Related

- `internal/mcp/server.go` (package skeleton, this ticket)
- `internal/query` (MCP-002)
- `internal/mcp` tool wiring (MCP-003) and `ragctl serve` (`internal/cli`)
- `internal/mcp/offline_test.go` (MCP-004)
