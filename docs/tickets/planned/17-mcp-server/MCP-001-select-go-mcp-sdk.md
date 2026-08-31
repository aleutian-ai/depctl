# MCP-001: Select Go MCP SDK

**Epic:** MCP Server
**Status:** planned
**Depends on:** core query services (MCP-002)
**Estimated size:** small

## Goal
Evaluate and pick one maintained Go MCP SDK with stable stdio and streamable-HTTP transport support, and record the decision.

## Non-goals
- Implementing any MCP tools (MCP-003) or the query service itself (MCP-002).
- Building a custom MCP protocol implementation from scratch — only do this if no viable SDK exists.

## Simplicity constraints
- Pick exactly one SDK. Do not build a transport-abstraction layer "in case we switch" — if a switch is ever needed, that's a follow-up ticket.
- Keep protocol/transport code confined to `internal/mcp`; domain packages must never import the SDK.

## Design
Write `docs/adr/ADR-006-mcp-primary-agent-interface.md` (or extend if it exists) documenting:
- SDK evaluated and chosen (with version/module path).
- Why: stdio + streamable HTTP support, maintenance activity, API stability.
- Package boundary: `internal/mcp` wraps the SDK; nothing else in the codebase references MCP types directly.

Add the chosen module to `go.mod` only in this ticket (no functional code yet beyond a package skeleton `internal/mcp/server.go` with an empty `New(...) *Server`).

## Inputs / Outputs
- Input: current landscape of Go MCP SDKs at implementation time.
- Output: ADR + `go.mod` dependency + empty `internal/mcp` package skeleton.

## Failure behavior
N/A (research/decision ticket).

## Tests
- `go build ./...` succeeds with the new dependency and empty skeleton package.

## Acceptance criteria
- [x] ADR written recording the chosen SDK and rationale.
- [x] No MCP/protocol types leak into `internal/domain` or other non-`internal/mcp` packages.
- [x] Repository builds cleanly with the new dependency added.
