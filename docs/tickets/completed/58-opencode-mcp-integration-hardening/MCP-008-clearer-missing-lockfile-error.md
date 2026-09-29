# MCP-008: Clearer missing-lockfile error text

**Epic:** OpenCode/MCP integration hardening
**Status:** done — implemented and live-verified 2026-09-27
**Depends on:** none
**Estimated size:** small

## Problem
A Node project with `package.json` but no supported lockfile (`package-lock.json`/`pnpm-lock.yaml`/`yarn.lock`/`bun.lock`) correctly fails to resolve — this is deliberate, already-correct behavior (ragctl never guesses a dependency version from a semver range) and needed no behavior change. Confirmed accurate in the reported session: `plugin/pi` failed with exactly this shape, and the agent correctly explained why without being told.

The only real gap was the error text not being maximally actionable — it named the problem but didn't say what to do next.

## What changed
`internal/resolver/node/resolve.go`'s no-lockfile error (`Resolve`'s fallthrough after trying every entry in `lockfilePriority`) now reads:
```
no supported lockfile found (package.json alone, with no lockfile, isn't resolvable yet —
run npm install, pnpm install, yarn install, or bun install to generate one of
package-lock.json, pnpm-lock.yaml, yarn.lock, or bun.lock, then re-run `ragctl scan`)
```
instead of the old, purely descriptive:
```
no supported lockfile found (package.json alone, with no lockfile, isn't resolvable yet)
```

This deliberately deviates from the original report's suggested multi-line/boxed format. Tracing where this error actually renders (`internal/cli/scan.go`: `fmt.Fprintf(out, "resolve error %-8s %s: %v\n", ...)`) showed every resolver error in this codebase — Go, Python, Node alike — is rendered as one `%v`-embedded line, not a multi-line block; a multi-line message would look broken wherever it's actually printed (`ragctl scan`'s own stdout, and `scan_project`'s streamed progress lines, which split on newlines). Kept as one sentence instead, matching the exact style `internal/resolver/python/resolve.go`'s own analogous "no supported lock/manifest file found... add a uv.lock, poetry.lock, or requirements.txt" message already established — a real precedent in this codebase, not a new convention invented for this ticket. Also corrected staleness in the original report's own suggested wording: it only named `package-lock.json`/`pnpm-lock.yaml`; `yarn.lock`/`bun.lock` support (`internal/resolver/node/yarn.go`, `bun.go`) was added later and is included here.

## Live verification, 2026-09-27
Built the real binary, ran `ragctl scan` against a real fixture directory (`package.json` present, no lockfile), confirmed the full message renders correctly end to end through the actual CLI output path (not just the unit-level error string):
```
resolve error node     /tmp/.../fixture: resolver node: resolve /tmp/.../fixture: no supported lockfile found (package.json alone, with no lockfile, isn't resolvable yet — run npm install, pnpm install, yarn install, or bun install to generate one of package-lock.json, pnpm-lock.yaml, yarn.lock, or bun.lock, then re-run `ragctl scan`)
```

## Non-goals
- No change to resolution behavior itself — refusing to guess stays exactly as-is.
- No lockfile generation, no package-manager invocation on ragctl's behalf.

## Acceptance criteria
- [x] Error text names every currently-supported lockfile (`package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`, `bun.lock`) and the command to produce each, plus the concrete next step (`ragctl scan` again).
- [x] `TestResolveNoLockfile` (`internal/resolver/node/resolve_test.go`) strengthened to assert every actionable detail is present, not just the original "no supported lockfile" substring.
- [x] Live-verified via a real `ragctl scan` invocation against a real fixture — confirmed the message renders correctly through the actual CLI output path, not just in isolation.
- [x] No behavior change: `TestResolveNoLockfile` and the rest of `internal/resolver/node`'s suite still pass unchanged in shape (same error is still returned, just with clearer text).
