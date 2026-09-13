# GIT-004: Local package-manager cache fast path

**Epic:** Local package-manager cache reuse
**Status:** backlog
**Depends on:** GIT-001 (git cache manager)
**Estimated size:** medium

## Goal
When a resolved dependency's source is already sitting in the local package manager's own cache (`$GOMODCACHE`, a scanned project's `node_modules`, `site-packages`), seed or skip `internal/source/git`'s mirror clone from that local copy instead of always re-cloning from the remote, cutting first-sync time and bandwidth for the common case where the user already has the dependency locally.

## Non-goals
- No change to what ragctl treats as the source of truth for version history — release-note/cross-version work still needs the full mirror clone; this only fast-paths acquiring one version's current tree.
- No custom cache-location configuration — use each ecosystem's own standard discovery (`go env GOMODCACHE`, project-relative `node_modules`, active virtualenv's `site-packages`), not a new config key users have to maintain.
- No Rust/Java scope — Go, Node, Python only, matching the resolvers that exist today.

## Simplicity constraints
- Purely additive to `internal/source/git.Cache`: a new optional "local seed" lookup tried before the network clone, never replacing the clone path — if the local cache is missing, stale, or the wrong version, fall straight back to today's behavior with no user-visible difference.
- No new domain types or registry changes — this is entirely inside the acquisition layer.

## Design
- `internal/source/git`'s `Cache` gains a `localSeed(ecosystem, pkg, version) (path string, ok bool)` hook per ecosystem, called before `git clone --mirror`.
  - Go: `go env GOMODCACHE`, look for `<module>@<version>` under it.
  - Node: the scanned project's own `node_modules/<pkg>`, version-checked against its `package.json`.
  - Python: the active interpreter's `site-packages/<pkg>` (or `dist-info` metadata), version-checked.
- A hit seeds the working tree ragctl reads from directly (no `.git` history needed for the current-version read path); a miss or version mismatch clones exactly as today.
- Log which path was taken (`local cache hit` vs `cloned`) — useful for profiling how much this actually saves in practice before investing further.

## Inputs / Outputs
- Input: a resolved dependency (ecosystem, package, version) about to be synced.
- Output: the same working tree normalization already reads from today, sourced from the local cache when available.

## Failure behavior
- Local cache present but version mismatch, corrupted, or unreadable: silently fall back to cloning — never error out because of a bad local cache.
- Release-note/cross-version generation that needs history beyond the current version: always uses the mirror clone, regardless of whether a local-cache fast path exists for the current version.

## Tests
- Go module present in `$GOMODCACHE` at the exact resolved version: no network clone happens, content matches what a clone would produce.
- Version mismatch between local cache and resolved version: falls back to cloning.
- No local cache present: behavior identical to today (regression check).
- Cross-version release-note generation still uses the full mirror clone even when a local-cache fast path was used for the "current" version.

## Acceptance criteria
- [ ] Go, Node, and Python each have a working local-cache fast path with the documented fallback behavior.
- [ ] No regression to release-note/cross-version generation, which still requires the full mirror clone.
- [ ] First-sync time/bandwidth measurably improves for a project whose dependencies are already present locally (recorded in the ticket's post-implementation note, not just asserted).
