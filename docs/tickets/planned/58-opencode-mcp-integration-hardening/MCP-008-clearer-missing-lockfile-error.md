# MCP-008: Clearer missing-lockfile error text

**Epic:** OpenCode/MCP integration hardening
**Status:** planned, low priority
**Depends on:** none
**Estimated size:** small

## Problem
A Node project with `package.json` but no supported lockfile (`package-lock.json`/`pnpm-lock.yaml`) correctly fails to resolve — this is deliberate, already-correct behavior (ragctl never guesses a dependency version from a semver range) and needs no behavior change. Confirmed accurate in the reported session: `plugin/pi` failed with exactly this shape, and the agent correctly explained why without being told.

The only real gap is the error text an agent (or a person) sees isn't maximally actionable — it names the problem but could more directly state what to do next.

## Design direction
Match the shape already suggested in the original report:
```
Project detected: Node
Resolution unavailable: package.json exists but no supported lockfile was found.

Expected one of:
- package-lock.json
- pnpm-lock.yaml

Run your package manager to produce a lockfile, then rescan.
```
Applied wherever the current resolver error surfaces today (`internal/resolver/node`'s own resolution-error message, and however `scan_project`/`ragctl scan` renders it).

## Non-goals
- No change to resolution behavior itself — refusing to guess stays exactly as-is.
- No lockfile generation, no package-manager invocation on ragctl's behalf.
