# STRESS-017: Daemon restart during an active MCP session

**Epic:** Full-Loop Stress Testing
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Goal
Restart the daemon (both gracefully and via `kill -9`) while a real `ragctl serve` MCP session is connected and mid-conversation with it, and confirm the client either reconnects/autostarts cleanly on the next tool call, or fails with a clear, actionable error — never a silent hang or a confusing partial response.

## Non-goals
- No change to `ensureDaemon`'s autostart logic unless this finds a real gap — verification first.

## Simplicity constraints
- Reuses VERIFY-001's real-subprocess-plus-real-client pattern.

## Design
1. Spawn a real `ragctl serve` subprocess, connect a real MCP client, issue one successful tool call to confirm the session is live.
2. `ragctl daemon stop` (graceful) — issue another tool call through the same, still-connected MCP session. Record what happens: does `ragctl serve` autostart a fresh daemon for the next call (matching its documented "invisible after `ragctl init`" behavior), or does the call fail?
3. Repeat with `kill -9 <daemon pid>` (ungraceful) instead of `daemon stop`.
4. In both cases, confirm a subsequent NEW `ragctl serve` session (not the one that was live during the restart) works normally afterward.

## Inputs / Outputs
- Input: a live MCP session, a daemon restart (graceful and ungraceful) mid-session.
- Output: pass/fail on the existing session's behavior (clean reconnect/autostart or clear error, never a hang) and on a fresh session working normally afterward.

## Failure behavior
- A hang, a confusing partial/malformed response, or a fresh session also failing afterward is this ticket's finding.

## Tests
- Manual/live exercise; results recorded in this ticket's post-implementation note.

## Acceptance criteria
- [ ] A tool call issued through an already-connected MCP session, after the daemon was restarted underneath it (both gracefully and via `kill -9`), either succeeds via clean reconnect/autostart or fails with a clear, actionable error — never a hang.
- [ ] A fresh MCP session started after either restart works normally.
