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
	patterns := append([]string{}, DocPatterns()...)
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
		// *.d.ts is NORM-008's own structured-extraction input — small,
		// declaration-only files (no implementation), cheap to always
		// fetch. A package with no .d.ts gets its plain-.js entry file
		// added separately (nodeJSEntryPatterns in build.go) for tsdoc's
		// JSDoc fallback, rather than fetching every .js file here.
		patterns = append(patterns, "package.json", "*.d.ts")
	}
	if ecosystem == domain.EcosystemPython {
		// *.py/*.pyi are NORM-009's own structured-extraction input.
		// Unlike npm's typical deep node_modules-style implementation
		// tree, a published PyPI package's own source is usually flat
		// and bounded (closer to a Go module's own shape than a Node
		// package's), so fetching it broadly — rather than needing an
		// entry-point-only two-phase acquisition the way NORM-008's
		// deferred JSDoc fallback would — is a reasonable, direct
		// analog of *.go's own precedent above.
		patterns = append(patterns, PythonSourcePatterns()...)
	}
	return patterns
}

// DocPatterns returns the doc-shaped file patterns every ecosystem gets
// regardless of any subdirectory scoping — split out from SparsePatterns
// so SUBDIR-001's acquisition (build.go's acquireGitSources) can keep
// these unscoped at the repo root even when Python source patterns are
// scoped to a discovered subdirectory. That split matters because a
// Python package found in a same-named subdirectory (SUBDIR-001) is
// usually not a monorepo submodule the way a Node discoverNodeSubdir
// match is — it's one package whose source happens to be nested, but
// whose real README/LICENSE/CHANGELOG still live at the true repo root,
// describing the package as a whole. Scoping doc patterns to the
// subdirectory too would silently exclude them, live-found: real
// pydantic's own repo has no README/LICENSE inside its `pydantic/`
// source directory at all.
func DocPatterns() []string {
	return []string{"*.md", "*.mdx", "*.txt", "*.rst", "LICENSE*", "license*"}
}

// PythonSourcePatterns returns just NORM-009's structured-extraction
// input patterns, split out from SparsePatterns for the same reason
// DocPatterns is — see its own doc comment.
func PythonSourcePatterns() []string {
	return []string{"*.py", "*.pyi"}
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
