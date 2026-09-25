# MCP-007: Distinguish a real sync failure from a bounded-wait timeout

**Epic:** OpenCode/MCP integration hardening
**Status:** planned
**Depends on:** none
**Estimated size:** small

## Problem
`syncProjectHandler`'s bounded-wait design (WATCH-018, `internal/mcp/tools.go`) only produces the graceful `{still_running: true, ...}` response on its own timer (`mcpSyncWaitBound`, 90s) firing. If the underlying `sync.SyncProject(...)` call returns a **real error** before that timer fires, it's wrapped and returned as a raw tool-level error instead:
```go
select {
case r := <-done:
	if r.err != nil {
		return nil, SyncProjectOut{}, fmt.Errorf("sync_project: %w", r.err)
	}
	...
case <-time.After(mcpSyncWaitBound):
	return nil, SyncProjectOut{StillRunning: true, ...}, nil
}
```
A raw tool-level error and a client's own timeout can look identical to an agent with no further context (both surface as "the call failed / took too long"), even though one means "the sync is fine, just not done yet" and the other means "something is genuinely wrong." Live-found: a reported `(32001)` timeout-shaped error on one Node plugin's `sync_project` call, in the same session where a different project's `sync_project` call correctly returned a graceful `still_running` response — suggesting these are two different code paths producing visually-similar symptoms, not the same bug.

## What this ticket needs before design
Reproduce the *specific* failure mode that produces a raw error here (not just a slow sync) — candidates include a genuine acquisition failure (no registry manifest, no matching tag — both legitimate, already-correct refusals elsewhere in the codebase) reaching this handler's error path instead of being retried/absorbed, or a real transport-level issue between the MCP process and the daemon. Until reproduced, a fix risks papering over the symptom (making all errors *look* like `still_running`) rather than fixing the actual gap.

## Design direction (not finalized)
Likely: give the agent enough information to tell the two cases apart even when a real error does occur before the bound fires — e.g. a distinct response shape for "the sync errored, here's what and for which dependency" versus today's single generic `fmt.Errorf` wrap, so a real failure is exactly as actionable as WATCH-018 already made a slow one.

## Non-goals
- No change to the `still_running` path itself — it already works correctly (confirmed live, the Go project's own sync in the same session).
- Not assumed to be the same root cause as MCP-004 (the `sync_progress` staleness bug) — related in symptom (both look like "sync seems to have failed silently"), not confirmed to share a cause.
