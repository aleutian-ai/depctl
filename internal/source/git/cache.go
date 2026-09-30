// Package git implements ragctl's local Git acquisition layer: one shared
// bare mirror per repository (cloned blobless via --filter=blob:none,
// GIT-005 — full history/tags transfer, file content is fetched lazily
// on checkout), on-demand tag fetches, ref-to-commit resolution,
// sparse-checkout-scoped disposable worktree materialization (only the
// doc-shaped files a normalizer would actually read, with a transparent
// fallback to a full checkout if the pattern set matches nothing), and
// file-level delta computation between two commits. Every operation
// shells out to the system git binary via internal/executil — no Git
// wire protocol or object parsing is implemented directly.
//
// SEC-004 invariant: this package never executes anything from inside a
// fetched repository. Every executil.Run call in this package invokes
// the git binary itself, against the mirror/worktree directories git
// itself manages — never a script or binary found within the fetched
// content. A dependency's own repository is data to git and to every
// normalizer that reads its checked-out files, never something ragctl
// runs.
package git

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aleutian-ai/ragctl/internal/config"
	"aleutian-ai/ragctl/internal/executil"
	"aleutian-ai/ragctl/internal/httplimit"
)

// defaultCloneTimeout bounds git clone/fetch, which can be slow over the
// network for large repositories.
const defaultCloneTimeout = 10 * time.Minute

// defaultLocalTimeout bounds git operations that never touch the network
// (rev-parse, worktree add/remove, diff).
const defaultLocalTimeout = 30 * time.Second

// Cache manages a local bare-mirror cache of Git repositories, one mirror
// per repository URL shared across every dependency version of that
// package.
type Cache struct {
	root string

	// externalMirrorRoots (GIT-006) are additional directories, laid
	// out identically to root (mirrorPath's <host>/<org>/<repo>.git
	// convention), checked in order before EnsureMirror falls through
	// to a network clone. Read-only from ragctl's perspective — a hit
	// is seeded into root via `git clone --local`, never read from or
	// written to in place.
	externalMirrorRoots []string

	// checkoutSearchRoots (GIT-007) are additional directories that may
	// contain arbitrary real git working-tree checkouts (not laid out
	// in any particular convention — e.g. a personal corpus like
	// ~/offline-knowledge), checked after externalMirrorRoots and before
	// a network clone. Discovered by walking each root and comparing a
	// found checkout's own "origin" remote against the URL being
	// acquired, not by path convention — see findCheckoutSeed.
	checkoutSearchRoots []string

	mirrorLocksMu sync.Mutex
	mirrorLocks   map[string]*sync.Mutex

	// maxMirrorBytes (SEC-003) bounds one repository's real, on-disk
	// mirror size — checked once right after a clone completes (seeded
	// or fresh network clone), never mid-transfer (git itself doesn't
	// expose a byte-budget knob for `clone --mirror`). 0 means unset —
	// config.DefaultMaxFetchSourceTotalBytes applies.
	maxMirrorBytes int64
}

// Option configures optional Cache behavior at construction time.
type Option func(*Cache)

// WithExternalMirrorRoots configures GIT-006's external mirror search
// path (see Cache.externalMirrorRoots' own doc comment).
func WithExternalMirrorRoots(roots []string) Option {
	return func(c *Cache) { c.externalMirrorRoots = roots }
}

// WithCheckoutSearchRoots configures GIT-007's external checkout search
// path (see Cache.checkoutSearchRoots' own doc comment).
func WithCheckoutSearchRoots(roots []string) Option {
	return func(c *Cache) { c.checkoutSearchRoots = roots }
}

// WithMaxMirrorBytes configures SEC-003's per-repository mirror size cap
// (see Cache.maxMirrorBytes' own doc comment).
func WithMaxMirrorBytes(max int64) Option {
	return func(c *Cache) { c.maxMirrorBytes = max }
}

// NewCache returns a Cache rooted at dir, e.g. "<data-dir>/git".
func NewCache(dir string, opts ...Option) *Cache {
	c := &Cache{root: dir, mirrorLocks: map[string]*sync.Mutex{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// lockMirror serializes every EnsureMirror call for the same repoPath,
// so two concurrent first-time acquisitions of the same repository (a
// real possibility once a planner drives multiple dependency builds
// concurrently) can't both run `git clone --mirror` into the same
// target directory at once — a shared resource across every generation
// of that dependency, not scoped to one build, so a corrupted mirror
// here would silently break every future build of that dependency, not
// just the racing ones. Returns an unlock func to defer.
func (c *Cache) lockMirror(repoPath string) func() {
	c.mirrorLocksMu.Lock()
	l, ok := c.mirrorLocks[repoPath]
	if !ok {
		l = &sync.Mutex{}
		c.mirrorLocks[repoPath] = l
	}
	c.mirrorLocksMu.Unlock()

	l.Lock()
	return l.Unlock
}

// EnsureMirror clones rawURL as a bare mirror under the cache root if one
// doesn't already exist locally; an existing mirror is left untouched —
// call FetchTags to update it.
//
// The clone uses --filter=blob:none (a blobless partial clone, GIT-005):
// full commit/tag history and tree metadata are still transferred in
// full — cross-version diffing needs that — but file content (blobs)
// is fetched lazily, only when something actually checks it out.
// Real hosts that don't support the filter (or a local git old enough
// not to know the flag at all) don't need special-casing here: a
// server lacking filter support just warns and clones unfiltered
// automatically (verified directly against a real git invocation while
// building this ticket) — only a local git literally too old to parse
// the flag fails outright, handled below with one plain retry.
func (c *Cache) EnsureMirror(ctx context.Context, rawURL string) (string, error) {
	repoPath, err := mirrorPath(c.root, rawURL)
	if err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindPermanent, Cause: err}
	}
	defer c.lockMirror(repoPath)()

	if _, statErr := os.Stat(repoPath); statErr == nil {
		return repoPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindPermanent, Cause: err}
	}

	// GIT-006: an external directory laid out exactly like c.root itself
	// (a backup/portable copy of another machine's ragctl cache) seeds
	// repoPath via a local clone, before ever trying the network. A bad,
	// missing, or corrupted external root — or a seed clone that fails —
	// falls straight through to the network clone below, never an error.
	if seedPath, ok := c.findExternalMirrorSeed(rawURL); ok {
		if res, err := executil.Run(ctx, executil.RunOptions{
			Dir: filepath.Dir(repoPath), Args: []string{"git", "clone", "--local", "--mirror", seedPath, repoPath}, Timeout: defaultCloneTimeout,
		}); err == nil && res.ExitCode == 0 {
			if err := c.enforceMirrorSizeLimit(repoPath); err != nil {
				return "", err
			}
			return repoPath, nil
		}
	}

	// GIT-007: a real git working-tree checkout somewhere under a
	// configured search root, matched by its own "origin" remote rather
	// than by path convention (see findCheckoutSeed) — for the case an
	// external directory isn't laid out like c.root at all. Same
	// never-block-acquisition principle as the tier above.
	if seedPath, ok := c.findCheckoutSeed(ctx, rawURL); ok {
		if res, err := executil.Run(ctx, executil.RunOptions{
			Dir: filepath.Dir(repoPath), Args: []string{"git", "clone", "--local", "--mirror", seedPath, repoPath}, Timeout: defaultCloneTimeout,
		}); err == nil && res.ExitCode == 0 {
			if err := c.enforceMirrorSizeLimit(repoPath); err != nil {
				return "", err
			}
			return repoPath, nil
		}
	}

	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     filepath.Dir(repoPath),
		Args:    []string{"git", "clone", "--mirror", "--filter=blob:none", rawURL, repoPath},
		Timeout: defaultCloneTimeout,
	})
	if err != nil || result.ExitCode != 0 {
		// A local git too old to know --filter at all is the only
		// realistic case that reaches here rather than degrading
		// gracefully on its own — retry once without it.
		os.RemoveAll(repoPath)
		result, err = executil.Run(ctx, executil.RunOptions{
			Dir:     filepath.Dir(repoPath),
			Args:    []string{"git", "clone", "--mirror", rawURL, repoPath},
			Timeout: defaultCloneTimeout,
		})
	}
	if err != nil {
		return "", &CacheError{Op: "EnsureMirror", Kind: ErrKindTransient, Cause: err}
	}
	if result.ExitCode != 0 {
		os.RemoveAll(repoPath)
		return "", &CacheError{
			Op:    "EnsureMirror",
			Kind:  ErrKindTransient,
			Cause: fmt.Errorf("git clone --mirror %s: %s", rawURL, bytes.TrimSpace(result.Stderr)),
		}
	}
	if err := c.enforceMirrorSizeLimit(repoPath); err != nil {
		return "", err
	}
	return repoPath, nil
}

// enforceMirrorSizeLimit (SEC-003) checks repoPath's real, on-disk size
// against maxMirrorBytes right after a clone completes, removing the
// mirror and returning a permanent (non-retryable) error if it's too
// large — a repository this big is a permanent, not transient,
// condition for this cache, so a plain retry would just fail the same
// way again.
func (c *Cache) enforceMirrorSizeLimit(repoPath string) error {
	max := c.maxMirrorBytes
	if max <= 0 {
		max = config.DefaultMaxFetchSourceTotalBytes
	}
	size, err := mirrorDirSize(repoPath)
	if err != nil {
		return nil // can't measure it — don't fail acquisition over a stat error
	}
	if size <= max {
		return nil
	}
	os.RemoveAll(repoPath)
	return &CacheError{
		Op:    "EnsureMirror",
		Kind:  ErrKindPermanent,
		Cause: fmt.Errorf("%w: mirror is %d bytes, over the %d byte limit", httplimit.ErrFetchLimitExceeded, size, max),
	}
}

// mirrorDirSize sums the real, on-disk size of every regular file under root.
func mirrorDirSize(root string) (int64, error) {
	var total int64
	err := filepath.Walk(root, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

// FetchTags fetches new tags/refs into an existing bare mirror at
// repoPath.
func (c *Cache) FetchTags(ctx context.Context, repoPath string) error {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "fetch", "--tags"},
		Timeout: defaultCloneTimeout,
	})
	if err != nil {
		return &CacheError{Op: "FetchTags", Kind: ErrKindTransient, Cause: err}
	}
	if result.ExitCode != 0 {
		return &CacheError{
			Op:    "FetchTags",
			Kind:  ErrKindTransient,
			Cause: fmt.Errorf("git fetch --tags: %s", bytes.TrimSpace(result.Stderr)),
		}
	}
	return nil
}

// ResolveRef resolves ref (a tag, branch, or commit-ish) to a commit SHA
// purely from local cache — no network access.
func (c *Cache) ResolveRef(ctx context.Context, repoPath, ref string) (string, error) {
	result, err := executil.Run(ctx, executil.RunOptions{
		Dir:     repoPath,
		Args:    []string{"git", "rev-parse", ref + "^{commit}"},
		Timeout: defaultLocalTimeout,
	})
	if err != nil {
		return "", &CacheError{Op: "ResolveRef", Kind: ErrKindPermanent, Cause: err}
	}
	if result.ExitCode != 0 {
		return "", &CacheError{
			Op:    "ResolveRef",
			Kind:  ErrKindPermanent,
			Cause: fmt.Errorf("git rev-parse %s: %s", ref, bytes.TrimSpace(result.Stderr)),
		}
	}
	return strings.TrimSpace(string(result.Stdout)), nil
}

// findExternalMirrorSeed (GIT-006) checks each configured
// externalMirrorRoots entry, in order, for rawURL's exact
// <host>/<org>/<repo>.git path — the same convention mirrorPath already
// computes for c.root itself. The first root containing a match wins;
// later roots aren't even checked. No scanning, no heuristics: a root
// not laid out this way simply never matches anything, by design (see
// GIT-007 for the directory-of-arbitrary-checkouts case).
func (c *Cache) findExternalMirrorSeed(rawURL string) (string, bool) {
	for _, root := range c.externalMirrorRoots {
		extPath, err := mirrorPath(root, rawURL)
		if err != nil {
			continue // malformed root config shouldn't block acquisition
		}
		if info, statErr := os.Stat(extPath); statErr == nil && info.IsDir() {
			return extPath, true
		}
	}
	return "", false
}

// findCheckoutSeed (GIT-007) walks each configured checkoutSearchRoots
// entry for a real git working-tree checkout whose own "origin" remote
// matches rawURL, stopping at the first directory containing a .git
// entry in each branch of the walk — never descending into a checkout's
// own working-tree content, matched or not. Returns "", false if none
// match, an expected miss, not an error.
func (c *Cache) findCheckoutSeed(ctx context.Context, rawURL string) (string, bool) {
	wantHost, wantPath, err := splitGitURL(rawURL)
	if err != nil {
		return "", false
	}

	for _, root := range c.checkoutSearchRoots {
		var found string
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil || found != "" {
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

// checkoutOriginHostPath reads checkoutDir's "origin" remote and splits
// it the same way mirrorPath does, so a checkout is matched by the same
// host+repo-path identity as everything else in this file — regardless
// of whether its origin is recorded as an HTTPS URL, SCP-like syntax, or
// with/without a trailing ".git".
func checkoutOriginHostPath(ctx context.Context, checkoutDir string) (host, path string, err error) {
	res, runErr := executil.Run(ctx, executil.RunOptions{
		Dir: checkoutDir, Args: []string{"git", "-C", checkoutDir, "remote", "get-url", "origin"}, Timeout: defaultLocalTimeout,
	})
	if runErr != nil || res.ExitCode != 0 {
		return "", "", fmt.Errorf("no origin remote at %s", checkoutDir)
	}
	return splitGitURL(strings.TrimSpace(string(res.Stdout)))
}

// mirrorPath computes the cache-relative path for rawURL, following the
// "<host>/<org>/<repo>.git" convention (e.g. github.com/grpc/grpc-go.git).
func mirrorPath(root, rawURL string) (string, error) {
	host, path, err := splitGitURL(rawURL)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("git: URL %q has no repository path", rawURL)
	}

	segments := strings.Split(path, "/")
	segments[len(segments)-1] += ".git"
	return filepath.Join(append([]string{root, host}, segments...)...), nil
}

// splitGitURL splits a repository URL into a host and a repository path.
// It accepts standard URLs (https://host/org/repo.git), SCP-like syntax
// (git@host:org/repo.git), and plain local filesystem paths (used by
// tests, and by anyone mirroring from a local path) — the latter are
// rooted under a fixed "local" host so the cache layout stays consistent.
func splitGitURL(rawURL string) (host, path string, err error) {
	if u, uerr := url.Parse(rawURL); uerr == nil && u.Scheme != "" && u.Host != "" {
		return u.Host, normalizeGitPath(u.Path), nil
	}
	if idx := strings.Index(rawURL, "@"); idx >= 0 {
		rest := rawURL[idx+1:]
		if colon := strings.Index(rest, ":"); colon >= 0 {
			return rest[:colon], normalizeGitPath(rest[colon+1:]), nil
		}
	}

	abs, aerr := filepath.Abs(rawURL)
	if aerr != nil {
		return "", "", fmt.Errorf("git: cannot parse repository URL %q", rawURL)
	}
	return "local", normalizeGitPath(filepath.ToSlash(abs)), nil
}

// normalizeGitPath strips a trailing ".git" suffix and leading/trailing
// slashes, so every caller comparing two URLs for the same repository
// (mirrorPath's own on-disk convention, GIT-006/GIT-007's matching
// logic) sees the same identity regardless of which of the two
// equally-common forms — "https://host/org/repo" vs
// "https://host/org/repo.git" — either one happened to be written as.
// This normalization used to live only inside mirrorPath itself, which
// was splitGitURL's sole caller until GIT-007 added a second one
// (checkoutOriginHostPath) that compared raw splitGitURL output
// directly — live-found (a real ~/offline-knowledge checkout's origin
// recorded with ".git", the resolved dependency URL without it) rather
// than caught by any synthetic test, since every existing fixture
// happened to use the same literal string on both sides of the
// comparison, masking the mismatch entirely.
func normalizeGitPath(path string) string {
	path = strings.TrimSuffix(path, ".git")
	return strings.Trim(path, "/")
}
