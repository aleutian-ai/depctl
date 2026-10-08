# GIT-004: Local package-manager cache fast path

**Epic:** Local package-manager cache reuse
**Status:** done — see Post-implementation note
**Depends on:** GIT-001 (git cache manager)
**Estimated size:** medium

## Goal
When a resolved dependency's source is already sitting in the local package manager's own cache (`$GOMODCACHE`, a scanned project's `node_modules`, `site-packages`), seed or skip `internal/source/git`'s mirror clone from that local copy instead of always re-cloning from the remote, cutting first-sync time and bandwidth for the common case where the user already has the dependency locally.

## Non-goals
- No change to what depctl treats as the source of truth for version history — release-note/cross-version work still needs the full mirror clone; this only fast-paths acquiring one version's current tree.
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
- A hit seeds the working tree depctl reads from directly (no `.git` history needed for the current-version read path); a miss or version mismatch clones exactly as today.
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
- [x] Go, Node, and Python each have a working local-cache fast path with the documented fallback behavior.
- [x] No regression to release-note/cross-version generation, which still requires the full mirror clone.
- [x] First-sync time/bandwidth measurably improves for a project whose dependencies are already present locally (recorded in the ticket's post-implementation note, not just asserted).

## Post-implementation note

Built with one real design correction found before writing any code, not after: the ticket's own sketch for Node ("the scanned project's own `node_modules/<pkg>`") and the implicit assumption for all three ecosystems ("check the package manager's own cache") don't actually hold the same way for all three.

**Go and Python are genuinely machine-global** (`go env GOMODCACHE`; the active `python3`'s `site-packages`) — no project context needed, `domain.Generation` (deliberately, since generations are shared across every project that depends on them, never scoped to one) has no project reference to check anyway. **Node's own package-manager caches are not extracted, readable source trees at all** — confirmed directly against real installs on this machine, not assumed: npm's `~/.npm/_cacache` is sha512-content-addressed compressed tarballs, and Bun's `~/.bun/install/cache/*.npm` files are Bun's own binary manifest-cache format. Using either would mean shipping real tarball/binary-extraction code, a materially bigger scope than "check if a directory exists." `node_modules` itself *is* directly readable, but it's inherently per-project — there is no Node equivalent of `GOMODCACHE`. Resolved by threading the *specific project whose sync action triggered this build* (`planner.Action.ProjectID`, resolved to its root in `internal/cli/sync.go`'s `syncVersion`) down through `generation.Build`/`acquireGitSources` as a new `projectRoot` parameter — `Build()`'s own signature still carries no permanent project reference, this is purely "which project asked for this specific build attempt," always a safe empty string if unresolvable.

A second simplification fell out of the first design pass, not added deliberately: a local-cache hit never needs any of `acquireGitSources`' subdir-discovery machinery (`discoverNodeSubdir`/`discoverPythonSubdir`, sparse-checkout pattern scoping) — unlike a git clone of a whole repository, a package-manager cache entry is by construction already scoped to exactly one package, with no monorepo siblings ever mixed in. A hit skips straight past `EnsureMirror`/ref resolution/`MaterializeWorktree` entirely.

Python's dist-info matching (`findDistInfoSeed`) deliberately never guesses an import directory from the distribution name — PyPI names routinely differ from their real import package (`PyYAML` imports as `yaml`, confirmed as the canonical real-world case and used directly in its own test) — a dist-info match with no usable `top_level.txt` is treated as a miss, not a best-effort guess that could silently seed from the wrong directory.

**Measured, not just asserted** (the ticket's own explicit requirement): a real dependency already extracted in this machine's real `$GOMODCACHE` from building depctl itself (`github.com/google/go-cmp@v0.6.0`), built once with the cache entry moved aside (forcing a genuine network clone) and once restored (a genuine GIT-004 hit), same dependency, same network, same machine: **1.18s (real clone) → 50ms (local-cache hit), a 23.5x speedup** for this one dependency. Real GOMODCACHE state confirmed restored correctly afterward; the measurement script itself was throwaway, not committed (network-dependent, temporarily touches real system cache state — not CI-safe).

Tests: `internal/data/generation/localcache_test.go` — unit tests for each ecosystem's probe (`goModCacheSeed`'s module-path escaping, proven with a real uppercase-letter module path so the `!lowercase` encoding is actually exercised, not just a lucky all-lowercase match; `nodeModulesSeed`'s version-match/mismatch/no-project-root cases; `findDistInfoSeed`'s name-normalization, version-mismatch, and never-guess-without-top_level.txt cases; `normalizePyDistName` directly) plus one end-to-end `Build`-level regression test (`TestBuildUsesLocalGoModCacheAndNeverClonesOverTheNetwork`) using a deliberately unreachable registry-source URL — verified rigorously: temporarily disabled the local-cache-seed branch, confirmed the test fails with the real bug shape (`Could not resolve host`), restored it.

`golang.org/x/mod` (already an indirect transitive dependency) promoted to direct via `go mod tidy`, for `module.EscapePath` — reusing Go's own module-cache path-encoding logic rather than reimplementing it.

Full suite green (`go build`/`vet`/`test`), `-race` clean.
