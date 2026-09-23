package normalize

import "aleutian-ai/ragctl/internal/domain"

// SparsePatterns returns git sparse-checkout patterns (non-cone syntax —
// plain gitignore-style globs, matching at any depth with no leading
// slash) covering every file this package's normalizers can read,
// mirrored by hand from each Normalizer's own Supports() extension/
// filename matching (markdown.Normalizer: .md/.mdx; plaintext.Normalizer:
// .txt/.rst/LICENSE*; releasenotes.Normalizer's known filenames are
// already a subset of those two extensions, so nothing separate is
// needed for it) rather than calling Supports() itself, which takes a
// domain.SourceSnapshot (real file content/metadata), not a bare
// filename — there's nothing to construct one from before a clone
// exists. Two LICENSE casings are listed explicitly because git's
// pattern matching is case-sensitive on a case-sensitive filesystem
// (Linux); plaintext.Normalizer's own check is case-insensitive
// (strings.ToLower), so the sparse pattern set must cover both to match
// it exactly, not just rely on git's core.ignorecase (which defaults to
// true on macOS but false on Linux).
func SparsePatterns(ecosystem domain.Ecosystem) []string {
	patterns := []string{"*.md", "*.mdx", "*.txt", "*.rst", "LICENSE*", "license*"}
	if ecosystem == domain.EcosystemGo {
		// godoc.Normalizer needs the actual .go source, not just files an
		// extension/filename matcher like markdown/plaintext would catch.
		//
		// go.mod is never itself normalized, but generation.normalizeSources
		// needs it present in the checked-out worktree to detect a nested
		// Go module boundary (a subdirectory with its own go.mod is a
		// separate, independently-versioned module, not content belonging
		// to the dependency being synced — see hasGoMod in build.go). A
		// go.mod is a few hundred bytes; the cost of always fetching it is
		// negligible next to what omitting it costs for a repo like
		// google-cloud-go, whose root module is one file (doc.go)
		// alongside 200+ sibling modules that would otherwise be
		// misattributed as the root module's own content.
		patterns = append(patterns, "*.go", "go.mod")
	}
	if ecosystem == domain.EcosystemNode {
		// package.json is never itself normalized, but it's how
		// generation.normalizeSources (and discoverNodeSubdir before it)
		// detects a nested npm package boundary — see hasGoMod's go.mod
		// comment for the equivalent Go reasoning. A few hundred bytes
		// per package is negligible next to what skipping it costs for a
		// monorepo whose registry metadata didn't report which
		// subdirectory the target package actually lives in.
		patterns = append(patterns, "package.json")
	}
	return patterns
}

// ScopeToSubdir narrows patterns to files under subdir (a repo-relative
// path) so a sparse checkout of a monorepo fetches only that module's
// tree. An empty subdir returns patterns unchanged.
func ScopeToSubdir(patterns []string, subdir string) []string {
	if subdir == "" {
		return patterns
	}
	scoped := make([]string, len(patterns))
	for i, p := range patterns {
		scoped[i] = "/" + subdir + "/**/" + p
	}
	return scoped
}
