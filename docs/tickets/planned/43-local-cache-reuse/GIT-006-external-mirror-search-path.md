# GIT-006: External mirror search path

**Epic:** Local package-manager cache reuse
**Status:** planned
**Depends on:** GIT-001 (git cache manager), GIT-005 (blobless clone — this ticket's seeded mirrors benefit from the same `--filter=blob:none` reasoning, though for a different reason; see Design)
**Estimated size:** small

## Goal
Let `EnsureMirror` (`internal/source/git/cache.go`) seed a dependency's mirror from an already-cloned copy sitting in one or more configured external directories, before ever hitting the network — for repos ragctl's own cache doesn't already have, but that exist somewhere local (a backup/USB drive, a shared read-only mirror set, a portable copy of another machine's ragctl cache). Distinct from GIT-004 (ecosystem-native package-manager caches — `GOMODCACHE`, `node_modules`, `site-packages`, discovered via each tool's own standard location, no config needed) and from what already works today with zero new code (`hack/fetch-corpus`'s default `-mirror-root` already points at `config.DefaultDataDir()/git` — ragctl's own cache — so repos fetched that way are already reused automatically via `EnsureMirror`'s existing "already exists, return it" check). This ticket is for the case an external directory *isn't* already ragctl's own cache: no standard discovery mechanism exists for that, so (unlike GIT-004) an explicit config key is the correct, minimal design here, not scope creep.

## Non-goals
- No change to what's authoritative for version history/cross-version diffing — a seeded mirror is still a real bare mirror in ragctl's own cache afterward (via `git clone --local`, below), not a special-cased read path; `FetchTags`/`ResolveRef`/`MaterializeWorktree` all work on it identically to a network-cloned mirror.
- No indexing, scanning, or URL-matching heuristics against the external directory's contents. A search root is checked by computing the exact same `<host>/<org>/<repo>.git` layout `mirrorPath` (`cache.go:169`) already uses for ragctl's own cache — if a matching path exists there, it's used; if not, this ticket does nothing further for that root. No "search for a repo that looks like it might be the right one."
- No mutation of the external directory, ever — it's read-only from ragctl's perspective (see Design: seeding always goes through a local `git clone`, which never writes back to its source).
- No change to GIT-004's own scope or Non-goals — GIT-004's "no custom cache-location configuration" reasoning is specific to caches every ecosystem's own tooling already publishes a standard discovery mechanism for (`go env GOMODCACHE`, etc.); an arbitrary external backup/mirror directory has no such standard location, so a config key is the right call here, not an exception to GIT-004's stance.

## Simplicity constraints
- One new `Cache` field, `externalMirrorRoots []string`, set once at construction (`NewCache` gains a variant or a setter — see Design) — not per-call configuration.
- `EnsureMirror` tries each configured root, in order, before the network clone — the very first hit wins; not a "pick the best/newest" comparison across multiple roots.
- Seeding is exactly one `git clone --local <external-mirror-path> <c.root's own computed path>` — reuses git's own same-filesystem hardlinking (fast, no real bytes duplicated when the external root is on the same disk) rather than ragctl reimplementing a copy routine. If the external and target paths are on different filesystems, `--local` transparently falls back to a real copy — still correct, just not free, and still entirely git's own well-tested logic, not ragctl's.

## Design
`internal/source/git/cache.go`:
```go
type Cache struct {
	root string
	// externalMirrorRoots are additional directories, laid out identically
	// to root (mirrorPath's <host>/<org>/<repo>.git convention), checked
	// in order before EnsureMirror falls through to a network clone.
	// Read-only from ragctl's perspective — a hit is seeded into root via
	// `git clone --local`, never read from or written to in place.
	externalMirrorRoots []string
	...
}
```
`EnsureMirror`'s existing "already exists locally" check (the `os.Stat(repoPath)` at the top, `cache.go:78`) gains a second lookup right before the network clone:
```go
for _, extRoot := range c.externalMirrorRoots {
	extPath, err := mirrorPath(extRoot, rawURL)
	if err != nil {
		continue // malformed root config shouldn't block acquisition
	}
	if _, statErr := os.Stat(extPath); statErr != nil {
		continue
	}
	if res, err := executil.Run(ctx, executil.RunOptions{
		Args: []string{"git", "clone", "--local", "--mirror", extPath, repoPath}, Timeout: defaultCloneTimeout,
	}); err == nil && res.ExitCode == 0 {
		return repoPath, nil
	}
	// seeding failed (corrupt external mirror, permissions) — fall
	// through to the network clone rather than erroring, same
	// "never let a bad cache block acquisition" principle GIT-004 uses.
}
```
Config: `git.mirror_search_paths []string` (new, `config.yaml`, empty by default — zero behavior change for anyone who doesn't set it). `runDaemonRun`/wherever `git.NewCache` is currently constructed passes `cfg.Git.MirrorSearchPaths` through to a `NewCache` that accepts them, or a small `WithExternalMirrorRoots(...)` option — whichever reads more naturally against `NewCache`'s existing single-argument shape once this is actually implemented.

Interaction with GIT-005: a seeded mirror still benefits from `MaterializeWorktree`'s sparse-checkout — the external source might be a *full* clone (no `--filter=blob:none`), but seeding into `c.root` via `git clone --local --mirror` from it is a local clone anyway, so the same "local clones don't transfer filtered content over a wire" reasoning GIT-005's post-implementation note already covers applies here too; the real savings are avoiding the *network* clone entirely, which this ticket delivers regardless of blob filtering.

## Inputs / Outputs
- Input: `git.mirror_search_paths` config (new); a rawURL about to be acquired that isn't yet in `c.root`.
- Output: `repoPath` seeded from the first matching external root, functionally identical to a network-cloned mirror for every downstream operation.

## Failure behavior
- No configured search paths (the default): identical to today's behavior — this ticket's whole code path is skipped.
- A configured root that doesn't exist, or a repo not present under it: falls through to the next root, then to the network clone — never an error.
- A matching path exists but `git clone --local` fails (corrupted external mirror, permissions issue): falls through to the network clone, not a fatal error — matching GIT-004's own "never let a bad local cache block acquisition" principle.

## Tests
- A repo present under a configured external root seeds `c.root` without any network access (assert via a fixture whose "remote" URL is deliberately unreachable — the seed must succeed anyway).
- Multiple configured roots: the first one containing the repo wins; a later root isn't even checked.
- An external root that exists but doesn't contain this particular repo: falls through correctly to the network clone.
- A corrupted/unreadable mirror under an external root: falls through to the network clone rather than erroring.
- No `mirror_search_paths` configured: `EnsureMirror`'s behavior and call sequence are byte-for-byte identical to before this ticket (regression guard against ever making this non-free-by-default).
- The seeded mirror supports `FetchTags`/`ResolveRef`/`MaterializeWorktree` identically to a network-cloned one (no special-cased read-only behavior leaks through).

## Acceptance criteria
- [ ] `git.mirror_search_paths` (new config, empty default) lets `EnsureMirror` seed from an already-cloned external mirror before attempting a network clone.
- [ ] Zero behavior change when unconfigured (the default for every existing installation).
- [ ] A bad/missing/corrupted external root never blocks acquisition — always falls through to the network clone.
- [ ] The external directory is never mutated — seeding always goes through `git clone --local`, read-only from the source's perspective.
