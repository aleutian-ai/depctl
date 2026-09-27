# MCP-005: Daemon build-version staleness detection

**Epic:** OpenCode/MCP integration hardening
**Status:** done
**Depends on:** none
**Estimated size:** small

## Goal
Give the daemon a real, per-build identity, and detect when the running daemon (ADR-011: one long-running process, reused by every later command) was started by a different build than the one talking to it now — so a route or tool added since it started produces a clear, diagnosable warning instead of a silent, unexplained failure.

## Problem
`ragctlVersion` was a hardcoded `const ragctlVersion = "v0.1.0"` — every build of `ragctl`, ever, reported the exact same string. This made any "is my daemon stale" check structurally impossible: two different builds could never be told apart. `ensureDaemon` (`internal/cli/daemon.go`) reuses whatever daemon is already reachable, unconditionally — if a user upgrades their `ragctl` install (git pull + rebuild, a new release) while an old daemon is still running from before, every later command silently keeps talking to it. A tool or route added since that daemon started genuinely doesn't exist on it.

This is the leading explanation for a live-found symptom: `sync_progress` returning 404 for an actively-syncing project, while every other tool (`scan_project`, `sync_project`, `list_project_dependencies`) worked correctly against the same daemon in the same session. `handleSyncProgress` itself cannot 404 for a valid request — it always responds 200, even with zero data — so a 404 that fires regardless of which project ID is passed, while everything else works, is exactly the signature of a daemon that predates the whole `/v1/sync/progress` route, not a bug in the route's own logic.

## Design
- `ragctlVersion` is now computed once via `runtime/debug.ReadBuildInfo()`'s VCS revision (`vcs.revision`, plus a `-dirty` suffix from `vcs.modified`) — Go automatically stamps this into any binary built with `go build` inside a git working tree (Go 1.18+, `-buildvcs=auto` by default), so no build tooling (Makefile, `hack/run.sh`, CI) needed to change. Falls back to `"unknown"` when build info genuinely isn't available (`go run`, no VCS) — never back to a fixed string, which would silently reintroduce this exact bug for any two such builds.
- `ensureDaemon` gains `warnIfVersionStale`, mirroring the existing `warnIfConfigStale` exactly: a one-line stderr warning naming both versions and the fix (`ragctl daemon stop`). Warn only, not auto-restart — auto-restarting would silently kill whatever the stale daemon might be mid-sync, which is worse than a warning the user can act on.
- `versionStaleWarning(pid int, daemonVersion, currentVersion string) string` is the pure decision function (empty string = no warning), matching `configIsStale`'s own separation of pure logic from I/O — directly unit-testable.
- Also surfaced in `daemon status`'s `version:` line (`versionFreshnessLabel`) and a new `doctor` check (`checkVersionFreshness`, "daemon build matches this command"), mirroring `configFreshnessLabel`/`checkConfigFreshness` exactly — the same information two different existing surfaces already give for config staleness.

## Non-goals
- No auto-restart of a stale daemon — see the epic's own non-goals.
- No change to `ragctlVersion`'s use anywhere else (still reported over `/v1/health` as `Version`, unchanged wire shape).

## Tests
- `versionStaleWarning`: matching versions → no warning; either side reporting `""` or `"unknown"` → no warning (no reliable signal, not a real mismatch); genuinely mismatched real versions → a warning naming the pid and both versions.
- `versionFreshnessLabel`: same matrix, rendered for the status line.
- `TestDaemonStatusFlagsStaleVersion`: a real end-to-end test — starts a real daemon subprocess, confirms `daemon status`/`doctor` report a current version, then simulates a rebuild (swapping the *current* process's own `ragctlVersion`, not the daemon's — exactly what a real upgraded binary talking to an old daemon looks like) and confirms both surfaces now report staleness.

## Post-implementation note
Found and fixed one real bug in this fix's own first version before it shipped: `go test` binaries aren't VCS-stamped the same way `go build` binaries are, so a test process's own `ragctlVersion` reads `"unknown"` — the first version of `versionStaleWarning` only guarded `daemonVersion == "unknown"`, not `currentVersion == "unknown"`, producing a false-positive warning in exactly this situation. Fixed by guarding both sides. This means any `ragctl` build lacking VCS info (not just test binaries — also a build from a tarball with no `.git`, or `go run`) is silently blind to staleness on that side of the comparison too — an accepted, disclosed degradation (no reliable signal, so no false claim), not a gap to chase further here.

Full suite green (build/vet/test), real end-to-end daemon test included.
