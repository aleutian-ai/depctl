# Epic: OpenCode/MCP integration hardening

**Found live:** a real, offline, from-scratch `ragctl` + OpenCode session against a real polyglot repo (`engram`: one Go project, two Node plugins), reported directly by the user with terminal screenshots as evidence, not a hypothetical. Two visible symptoms: `sync_progress` returned 404 for an actively-syncing project, and `sync_project` on one Node plugin returned a raw `(32001)` timeout-shaped error instead of a graceful bounded response.

Each ticket below was individually verified against the real code (or live-reproduced) before being written — several plausible-looking theories were tried and *rejected* by evidence along the way; see each ticket's own note for what was actually confirmed and how.

## Tickets
- [MCP-004](MCP-004-stale-sync-progress-after-fast-followup.md) — **done.** `sync_progress` reported "0 failed" after a real 2-failure sync. Live-reproduced, root-caused, and fixed twice — the first fix (progress-object replacement on a new run) was real but incomplete; continuing to live-verify after it passed found the actual mechanism (`NOOP` planner actions being counted as successful syncs). Both are fixed and live-confirmed together.
- [MCP-005](MCP-005-daemon-build-version-staleness.md) — **done.** The daemon (a long-running singleton, ADR-011) had no way to detect it was started by an older build than the command talking to it — the leading, evidence-supported explanation for the reported `sync_progress` 404 specifically (a route that genuinely didn't exist yet on an old daemon, not a bug in the route itself).
- [MCP-006](MCP-006-auto-scan-on-serve-startup.md) — **planned, needs a product decision.** `ragctl serve` doesn't automatically scan its own working directory — `scan_project` must be called explicitly. Confirmed accurate; not obviously a bug (real tradeoffs), not started.
- [MCP-007](MCP-007-distinguish-real-failure-from-timeout.md) — **planned.** `sync_project`'s bounded-wait design (WATCH-018) only returns a graceful `still_running` response on a timeout; a genuine underlying failure surfaces as a raw tool-level error instead, which can look identical to "timed out" to an agent with no further context.
- [MCP-008](MCP-008-clearer-missing-lockfile-error.md) — **planned, low priority.** The missing-Node-lockfile case is already correct behavior (refuses to guess versions) — the only real gap is the error text isn't as actionable as it could be.

## Confirmed NOT a bug — no ticket
**"Fresh repo did not automatically bootstrap ragctl."** Tested live, twice, from a genuinely fresh machine (no `ragctl init` ever run): both `ragctl scan` and `ragctl serve` auto-created config, storage, and the daemon with zero manual steps. This already works correctly. Likely explanation for why it looked broken in the reported session: MCP-005's own root cause (a stale daemon from an earlier, different build) — bootstrapping had already happened at some point before, so the *symptom* was daemon staleness wearing a "nothing initialized" appearance, not actually a missing init path.

## Non-goals (this epic)
- No change to `ragctl sync` (the CLI command's own blocking behavior) — matches WATCH-018's own non-goal, unaffected by anything here.
- No new daemon HTTP endpoints beyond what MCP-004 needs — the existing read-only tools remain the correct way to check state.
- No auto-restart of a stale daemon (MCP-005) — a deliberate choice to warn rather than silently kill whatever the stale daemon might be mid-sync; revisit only if warning-only proves insufficient in practice.
