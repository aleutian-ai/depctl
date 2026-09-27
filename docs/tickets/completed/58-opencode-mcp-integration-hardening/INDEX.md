# Epic: OpenCode/MCP integration hardening

**Found live:** a real, offline, from-scratch `ragctl` + OpenCode session against a real polyglot repo (`engram`: one Go project, two Node plugins), reported directly by the user with terminal screenshots as evidence, not a hypothetical. Two visible symptoms: `sync_progress` returned 404 for an actively-syncing project, and `sync_project` on one Node plugin returned a raw `(32001)` timeout-shaped error instead of a graceful bounded response.

Each ticket below was individually verified against the real code (or live-reproduced) before being written — several plausible-looking theories were tried and *rejected* by evidence along the way; see each ticket's own note for what was actually confirmed and how.

## Tickets
- [MCP-004](MCP-004-stale-sync-progress-after-fast-followup.md) — **done.** `sync_progress` reported "0 failed" after a real 2-failure sync. Live-reproduced, root-caused, and fixed twice — the first fix (progress-object replacement on a new run) was real but incomplete; continuing to live-verify after it passed found the actual mechanism (`NOOP` planner actions being counted as successful syncs). Both are fixed and live-confirmed together.
- [MCP-005](MCP-005-daemon-build-version-staleness.md) — **done.** The daemon (a long-running singleton, ADR-011) had no way to detect it was started by an older build than the command talking to it — the leading, evidence-supported explanation for the reported `sync_progress` 404 specifically (a route that genuinely didn't exist yet on an old daemon, not a bug in the route itself).
- [MCP-006](MCP-006-auto-scan-on-serve-startup.md) — **done.** `ragctl serve` now auto-runs registration + a status summary against its own cwd on startup (cheap — manifest parsing/version resolution only, no clone/embed), before an agent ever calls a tool — but never auto-syncs; the agent/user still decides what to actually pull, via `sync_project`'s existing all/some/one scoping. Decided and implemented 2026-09-27.
- [MCP-007](MCP-007-distinguish-real-failure-from-timeout.md) — **done.** `sync_project`'s bounded-wait design (WATCH-018) only returned a graceful `still_running` response on a timeout; a genuine underlying failure surfaced as a raw, unclassified tool-level error instead. Root cause: the daemon's streaming NDJSON error line (used by `sync_project`/`scan_project`/`gc`) dropped error identity entirely, unlike the non-streaming path's existing `Kind`-based sentinel round-trip (WATCH-019/020) — fixed by extending that same mechanism to streaming responses, plus routing `sync_project`/`prioritize_file`'s errors through `toolError` like every other tool. Live-verified against a real daemon over the actual HTTP boundary.
- [MCP-008](MCP-008-clearer-missing-lockfile-error.md) — **done.** The missing-Node-lockfile case is already correct behavior (refuses to guess versions) — the only real gap was the error text not being as actionable as it could be. Now names every supported lockfile (`package-lock.json`/`pnpm-lock.yaml`/`yarn.lock`/`bun.lock`, matching a later addition the original report predated) and the command to produce each. Live-verified via a real `ragctl scan` run.

**All five tickets done — epic closed, 2026-09-27.**

## Confirmed NOT a bug — no ticket
**"Fresh repo did not automatically bootstrap ragctl."** Tested live, twice, from a genuinely fresh machine (no `ragctl init` ever run): both `ragctl scan` and `ragctl serve` auto-created config, storage, and the daemon with zero manual steps. This already works correctly. Likely explanation for why it looked broken in the reported session: MCP-005's own root cause (a stale daemon from an earlier, different build) — bootstrapping had already happened at some point before, so the *symptom* was daemon staleness wearing a "nothing initialized" appearance, not actually a missing init path.

## Non-goals (this epic)
- No change to `ragctl sync` (the CLI command's own blocking behavior) — matches WATCH-018's own non-goal, unaffected by anything here.
- No new daemon HTTP endpoints beyond what MCP-004 needs — the existing read-only tools remain the correct way to check state.
- No auto-restart of a stale daemon (MCP-005) — a deliberate choice to warn rather than silently kill whatever the stale daemon might be mid-sync; revisit only if warning-only proves insufficient in practice.
