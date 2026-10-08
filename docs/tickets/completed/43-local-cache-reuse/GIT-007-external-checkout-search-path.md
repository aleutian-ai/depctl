# GIT-007: External checkout search path

**Epic:** Local package-manager cache reuse
**Status:** done — see Post-implementation note
**Depends on:** GIT-001 (git cache manager); GIT-006 (external mirror search path — a sibling fallback tier, not a prerequisite in the strict sense, but `EnsureMirror` runs both in the same fallback chain, so GIT-006 should land first to keep the chain's ordering easy to reason about)
**Estimated size:** small-medium

## Goal
Let `EnsureMirror` (`internal/source/git/cache.go`) seed a dependency's mirror from an already-checked-out real git working tree sitting somewhere under one or more configured directories — e.g. a personal corpus like `~/offline-knowledge` (built by `hack/fetch-corpus`, organized `<category>/<reponame>`, not by URL) — before ever hitting the network.

This is deliberately **not** GIT-006. GIT-006 only matches an external directory laid out in depctl's own exact `<host>/<org>/<repo>.git` bare-mirror convention — a cheap, O(1), zero-scanning path check, correct for "a backup of depctl's own cache." A directory like `offline-knowledge` isn't laid out that way at all: it's an arbitrary tree of real working-tree checkouts, discoverable only by inspecting each one's own `git remote get-url origin`, not by path convention. That's a genuinely different mechanism — a bounded directory walk plus a URL comparison, not a path computation — with its own failure modes GIT-006 never has to consider (which checkout to trust if a root contains more than one repo with a similar name, how much of the tree to actually walk). It earns its own ticket rather than quietly expanding GIT-006's scope past its own stated Non-goals ("no indexing, scanning, or URL-matching heuristics").

## Non-goals (deliberately deferred past this ticket's simplest version)
- **No caching of the discovered URL→path mapping across calls.** Every miss (a dependency not already in `c.root`, and not found via GIT-006's exact-path check) walks the configured search roots fresh. This is intentionally the simplest correct thing to ship first — the walk only ever runs for a dependency depctl has never cloned before (an existing mirror short-circuits in `EnsureMirror`'s existing top check, before any of this runs), so the cost is paid once per dependency's *lifetime*, not per sync. If a real large corpus (hundreds of checkouts) measurably slows down a big first-time batch sync, an in-memory (per-`Cache`-instance, built-once) index is the obvious follow-up — not built now, because it's not needed to prove the mechanism works.
- **No handling of a checkout's working-tree state (dirty files, wrong currently-checked-out branch).** Seeding clones the checkout's *object store* (`git clone --local --mirror <checkout-dir> <repoPath>` — the same call GIT-006 uses, just pointed at a discovered working tree instead of a discovered bare mirror), not its current working-tree file contents. A dirty tree or a checkout currently sitting on the wrong branch is irrelevant: the clone transfers refs/objects, and whether the specific commit/tag this dependency needs actually exists in that object store is exactly the same "might miss, fall through to network" case GIT-006 already has for a stale/incomplete external mirror.
- **No discovery of nested repos.** The walk stops descending the moment it finds a directory containing a `.git` entry (file or directory — both work identically with `git clone --local`, since git itself resolves a worktree's `.git` *file* pointer transparently) and treats that directory as the one candidate checkout there. A submodule or nested repo underneath it is never separately discovered. Simplicity boundary, not an oversight.
- **No ambiguity resolution beyond "first match wins."** If a search root somehow contains two checkouts of the same remote URL, whichever the walk reaches first is used — no comparison of which is "better" (freshest, most complete history, etc.), matching GIT-006's own "first root wins, not a best-of comparison" stance.
- No change to what's authoritative for version history/cross-version diffing — same as GIT-004/006, a seeded mirror is a real bare mirror in `c.root` afterward, indistinguishable from a network-cloned one to every downstream operation (`FetchTags`/`ResolveRef`/`MaterializeWorktree`).
- No mutation of the external directory, ever — `git clone --local` never writes back to its source, same guarantee GIT-006 relies on.
- No new domain types or registry changes — entirely inside `internal/source/git`.

## Simplicity constraints
- One new `Cache` field, `checkoutSearchRoots []string` — same shape and construction pattern as GIT-006's `externalMirrorRoots`, not a separate concept at the `Cache` level.
- Runs strictly *after* GIT-006's exact-path check and *before* the network clone in `EnsureMirror`'s fallback chain — cheapest/most-certain checks first.
- The walk itself: `filepath.WalkDir` from each configured root, returning `fs.SkipDir` the instant a directory is found to contain a `.git` entry (checked via a plain `os.Lstat` for `.git` inside the current directory, not a full git invocation) — this is what keeps the walk cheap and bounded: it never descends into a checkout's own (potentially large) working-tree content, only into directories that aren't themselves already-found checkouts.
- URL matching reuses `splitGitURL` (already in `cache.go`) — the exact same host+path normalization `mirrorPath` already uses, so "does this checkout match this dependency" is answered with logic that already exists and is already tested, not new URL-parsing code.
- One `git` invocation per discovered checkout candidate (`git -C <dir> remote get-url origin`) — no `.git/config` file parsing, no `go-git` or other library dependency, matching this package's existing "shell out to the real git binary" convention throughout `cache.go`/`worktree.go`.

## Design
`internal/source/git/cache.go`:
```go
type Cache struct {
	root                string
	externalMirrorRoots []string // GIT-006
	checkoutSearchRoots []string // GIT-007 — this ticket
	...
}
```

`EnsureMirror`'s fallback chain, in order, once both GIT-006 and this ticket exist:
1. `os.Stat(repoPath)` — already have it (existing).
2. GIT-006: exact `<host>/<org>/<repo>.git` path under each `externalMirrorRoots` entry.
3. **GIT-007 (this ticket):** `findCheckoutSeed(ctx, rawURL)` — walk each `checkoutSearchRoots` entry, return the first checkout directory whose `origin` matches `rawURL`; seed via `git clone --local --mirror <checkout> <repoPath>`.
4. Network clone (existing).

```go
// findCheckoutSeed walks each configured checkoutSearchRoots entry for a
// real git working-tree checkout whose own "origin" remote matches
// rawURL, stopping at the first directory containing a .git entry in
// each branch of the walk (never descending into a checkout's own
// working-tree content). Returns "", false if none match — a plain,
// expected miss, not an error.
func (c *Cache) findCheckoutSeed(ctx context.Context, rawURL string) (string, bool) {
	wantHost, wantPath, err := splitGitURL(rawURL)
	if err != nil {
		return "", false
	}

	for _, root := range c.checkoutSearchRoots {
		var found string
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || found != "" {
				return nil // best-effort: an unreadable subtree is skipped, not fatal
			}
			if !d.IsDir() {
				return nil
			}
			if _, statErr := os.Lstat(filepath.Join(path, ".git")); statErr != nil {
				return nil // not a checkout root yet, keep descending
			}
			if host, p, err := checkoutOriginHostPath(ctx, path); err == nil && host == wantHost && p == wantPath {
				found = path
			}
			return filepath.SkipDir // never descend into a checkout's own tree, match or not
		})
		if found != "" {
			return found, true
		}
	}
	return "", false
}

// checkoutOriginHostPath reads path's "origin" remote and splits it the
// same way mirrorPath does, so a checkout is matched by the same
// host+repo-path identity as everything else in this file — regardless
// of whether its origin is recorded as an HTTPS URL, SCP-like syntax, or
// with/without a trailing ".git".
func checkoutOriginHostPath(ctx context.Context, checkoutDir string) (host, path string, err error) {
	res, runErr := executil.Run(ctx, executil.RunOptions{
		Args: []string{"git", "-C", checkoutDir, "remote", "get-url", "origin"}, Timeout: defaultLocalTimeout,
	})
	if runErr != nil || res.ExitCode != 0 {
		return "", "", fmt.Errorf("no origin remote at %s", checkoutDir)
	}
	return splitGitURL(strings.TrimSpace(string(res.Stdout)))
}
```

`EnsureMirror` itself gains one more fallthrough step, structurally identical to GIT-006's:
```go
if seed, ok := c.findCheckoutSeed(ctx, rawURL); ok {
	if res, err := executil.Run(ctx, executil.RunOptions{
		Args: []string{"git", "clone", "--local", "--mirror", seed, repoPath}, Timeout: defaultCloneTimeout,
	}); err == nil && res.ExitCode == 0 {
		return repoPath, nil
	}
	// seeding failed — fall through to the network clone, same principle
	// as every other fallback tier in this chain.
}
```

Config: `git.checkout_search_paths []string` (new, `config.yaml`, empty by default — zero behavior change for anyone who doesn't set it), a sibling key to GIT-006's `git.mirror_search_paths` under the same new `GitConfig` section (`internal/config/config.go`). Threaded into `git.NewCache` the same way GIT-006's field is — whichever option shape GIT-006 actually lands with, this ticket follows it rather than inventing a second construction pattern.

## Inputs / Outputs
- Input: `git.checkout_search_paths` config (new); a `rawURL` about to be acquired that isn't yet in `c.root` and didn't match GIT-006's exact-path check.
- Output: `repoPath` seeded from the first matching checkout found, functionally identical to a network-cloned mirror for every downstream operation.

## Failure behavior
- No configured search paths (the default): this ticket's whole code path is skipped — identical to today's behavior.
- A configured root that doesn't exist, is unreadable, or contains no matching checkout: falls through to the network clone, never an error.
- A checkout is found but has no `origin` remote, or `git remote get-url origin` fails for any reason: that candidate is skipped, the walk continues (within the same root, other branches; across roots, the next configured one).
- A checkout matches by URL but `git clone --local --mirror` from it fails (e.g. its object store is missing the commit/tag this dependency actually needs): falls through to the network clone, not a fatal error — the exact same principle GIT-004/006 already use.

## Tests
- A checkout present two levels under a configured root (matching `offline-knowledge`'s real `<category>/<reponame>` shape) whose `origin` matches the dependency's URL: seeds `c.root` without any network access (assert via a fixture whose "remote" URL is deliberately unreachable).
- A checkout present but whose `origin` does *not* match: walk continues past it, falls through to the network clone (or to a different, later checkout that does match, in a second variant of this test).
- The walk never descends into a discovered checkout's own subdirectories — a canary file placed deep inside a real checkout's tree must never be visited by the walk (asserted via a counting/logging fs.WalkDirFunc wrapper in the test, not by timing).
- Multiple configured roots: the first root containing a match wins; a later root isn't even checked.
- A configured root that doesn't exist on disk at all: no error, falls through correctly.
- A checkout with no `origin` remote configured: skipped, not fatal.
- No `git.checkout_search_paths` configured: `EnsureMirror`'s behavior and call sequence are byte-for-byte identical to before this ticket (regression guard, same as GIT-006's own equivalent test).
- The seeded mirror supports `FetchTags`/`ResolveRef`/`MaterializeWorktree` identically to a network-cloned one.

## Acceptance criteria
- [x] `git.checkout_search_paths` (new config, empty default) lets `EnsureMirror` seed from a real git working-tree checkout discovered under a configured root, matched by comparing its `origin` remote to the dependency's URL — not by path convention.
- [x] The walk never descends into a discovered checkout's own working-tree content.
- [x] Zero behavior change when unconfigured (the default for every existing installation).
- [x] A missing/unreadable root, a non-matching checkout, or a checkout whose object store is missing the needed commit never blocks acquisition — always falls through to the next tier, ultimately the network clone.
- [x] The external directory is never mutated.
- [x] Measured, not just asserted: seeding a real dependency from a real `offline-knowledge`-shaped checkout avoids the network clone (recorded in this ticket's post-implementation note).

## Post-implementation note

Built as designed — `findCheckoutSeed`/`checkoutOriginHostPath` (`internal/source/git/cache.go`) match the ticket's own sketch almost verbatim. `NewCache` gained `WithCheckoutSearchRoots(...)`, the sibling option to GIT-006's `WithExternalMirrorRoots(...)`, no constructor signature change needed since GIT-006 already introduced the variadic-`Option` pattern for exactly this reason. `git.checkout_search_paths` lives in the same `GitConfig` section GIT-006 added.

**A real bug, found only by the ticket's own required real-world measurement, not by any synthetic test.** The first live run against the actual `~/offline-knowledge/go/terraform` checkout showed *zero* speedup — `findCheckoutSeed` was silently missing the match entirely (`ok=false`), falling all the way through to a real network clone every time. Root cause: `splitGitURL` (`cache.go`) has always returned each URL form's *raw* path — its longtime sole caller, `mirrorPath`, stripped the `.git` suffix and trimmed slashes itself, immediately after calling it. `findCheckoutSeed`'s new `checkoutOriginHostPath` compared `splitGitURL`'s raw output directly, with no equivalent normalization of its own. Every synthetic test fixture for this ticket happened to use the *identical literal URL string* (including whether it had `.git` or not) on both the "resolved dependency URL" side and the "checkout's own origin" side — completely masking the mismatch. The real `offline-knowledge` checkout's actual origin was recorded as `https://github.com/hashicorp/terraform.git` (with `.git`, from however it was originally cloned); the query URL used in the measurement had no suffix — two equally common, everyday forms of the same repository URL that must compare equal and didn't.

**Fixed at the root, not patched at the call site:** moved the `.git`-suffix-strip + slash-trim normalization *into* `splitGitURL` itself (a new `normalizeGitPath` helper, applied in all three of its branches — scheme+host, SCP-like, and local-path), so every current and future caller gets consistent identity comparison for free, rather than each new caller needing to remember to normalize separately the way `mirrorPath` alone used to. `mirrorPath`'s own now-redundant trimming was removed. Two new regression tests (`internal/source/git/cache_test.go`): `TestSplitGitURLNormalizesGitSuffixConsistently` (the exact real-world mismatch) and `TestSplitGitURLNormalizesForSCPAndLocalFormsToo` (the other two branches) — verified rigorously: reverted the fix, confirmed both fail with the real mismatch shown directly in the failure message, restored it. Full suite re-run clean afterward — no other test relied on the old, unnormalized behavior.

**Measured, for real, after the fix**: seeding `github.com/hashicorp/terraform` from the real `~/offline-knowledge/go/terraform` checkout (a real, large, 172MB `.git`) vs. a real network clone of the same repository: **15.4s (network) → 6.1s (GIT-007 seed), a 2.5x speedup.** Smaller than GIT-004's 23.5x for a small Go module — honestly, not surprisingly: the seed step still does a real `git clone --local --mirror` of a genuinely large object store (this specific checkout also turned out to be a shallow clone, which makes `--local` fall back to a real copy instead of git's usual same-filesystem hardlinking — a real, disclosed limitation of this specific fixture, not of the mechanism itself), unlike GIT-004's Go/Python cases, which just point at an already-extracted directory with no clone step at all.

Tests (`internal/source/git/checkout_search_test.go`): a real seed hit two levels under a root (matching `offline-knowledge`'s own `<category>/<reponame>` shape); a checkout present but not matching (falls through to a real fallback clone); a genuine pruning regression test — a nested repo *inside* an outer, non-matching checkout that *would* match, proving the walk stops at the outer checkout's own `.git` and never finds the nested one (verified rigorously: removed the `filepath.SkipDir` return, confirmed the test fails by wrongly finding the nested match, restored it); multiple roots with the first match winning by content, not just by success; a configured root with no matching checkout; a checkout with no `origin` remote at all.

Full suite green (`go build`/`vet`/`test`), `-race` clean.
