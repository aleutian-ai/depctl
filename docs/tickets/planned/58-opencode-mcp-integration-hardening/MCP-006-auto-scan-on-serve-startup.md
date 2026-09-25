# MCP-006: Auto-scan on `serve` startup

**Epic:** OpenCode/MCP integration hardening
**Status:** planned — needs a product decision, not started
**Depends on:** none
**Estimated size:** small (once decided)

## Problem, confirmed accurate
`ragctl serve` does not automatically scan its own working directory. `scan_project` (`internal/mcp/tools.go`) is a tool the agent must explicitly call — confirmed by reading `runServe` (`internal/cli/serve.go`): its only use of `os.Getwd()` is for `explain_call_site`'s symbol-graph resolver, not project registration. In the reported session, the agent correctly called `scan_project` on its own (helped by the tool's own description) and it worked — but the user experience still requires that explicit step.

## The real decision
Making this fully automatic has genuine tradeoffs, not an obviously-correct default:
- Not every `ragctl serve` invocation happens inside a project directory worth scanning (e.g. a general-purpose session, or a directory the user doesn't want indexed yet).
- An automatic scan on every `serve` startup has a real cost (walks the directory tree, resolves dependencies) paid even for sessions that never ask about dependencies at all.
- `scan_project`'s own description already tells the agent to call it — the gap may be narrower than "add automatic scanning," e.g. making that guidance even more prominent, or auto-scanning only when the agent's *first* relevant tool call implies it needs project context (mirroring `search_dependency_docs`' own JIT-sync-on-miss pattern, applied to scanning instead of syncing).

## Options to weigh (not decided here)
1. Auto-scan cwd unconditionally on `serve` startup.
2. Auto-scan cwd lazily, on first tool call that needs project context (mirrors the JIT pattern already established elsewhere).
3. No behavior change — improve `scan_project`'s tool description further if real sessions keep showing the agent doesn't call it proactively (not currently evidenced — it worked correctly in the one real session reviewed).

## Non-goals
- No decision made in this ticket — it exists to record the finding and the real tradeoff, not to pick an option unilaterally.
