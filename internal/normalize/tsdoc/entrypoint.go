package tsdoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// packageJSONFields is the subset of package.json entryDeclarationFile/
// entryJSFile/packageDisplayName need.
type packageJSONFields struct {
	Name    string `json:"name"`
	Types   string `json:"types"`
	Typings string `json:"typings"` // older alias for "types"
	Main    string `json:"main"`
	Module  string `json:"module"`
}

// packageDisplayName resolves dir's real published package name from
// package.json's own "name" field — live-found: the previous fallback,
// filepath.Base(dir), returns the worktree's own randomly-named temp
// directory when a package has no Subdir scoping (nothing meaningful to
// base a name on), and even when a subdir IS present, a directory
// basename can still disagree with the package's real name (nested path
// segments, or simply not matching) and can never represent a scoped
// package ("@foo/bar") correctly at all. package.json's "name" is the
// one authoritative source for what the package actually calls itself,
// already read for other purposes elsewhere in this package — reused
// here, with the old directory-basename behavior kept only as a
// last-resort fallback for the (rare) case package.json is unreadable
// or declares no name.
func packageDisplayName(dir string) string {
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg packageJSONFields
		if err := json.Unmarshal(data, &pkg); err == nil && pkg.Name != "" {
			return pkg.Name
		}
	}
	return filepath.Base(dir)
}

// entryDeclarationFile resolves dir's published .d.ts entry point:
// package.json's own "types"/"typings" field when present (the
// authoritative signal — that field exists specifically for this),
// otherwise a same-basename .d.ts sitting next to "main", otherwise the
// first .d.ts file found directly in dir. Bounded and non-recursive by
// design — no bundler-level `exports`-map resolution, matching NORM-008's
// own Non-goals.
func entryDeclarationFile(dir string) (string, bool) {
	var pkg packageJSONFields
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}

	types := pkg.Types
	if types == "" {
		types = pkg.Typings
	}
	if types != "" {
		if p := filepath.Join(dir, filepath.FromSlash(types)); fileExists(p) {
			return p, true
		}
	}
	if pkg.Main != "" {
		mainPath := filepath.FromSlash(pkg.Main)
		p := filepath.Join(dir, strings.TrimSuffix(mainPath, filepath.Ext(mainPath))+".d.ts")
		if fileExists(p) {
			return p, true
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".d.ts") {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

// entryJSFile resolves dir's plain-JS entry point for the JSDoc fallback
// (NORM-008's deferred scope, closed here) — used only once
// entryDeclarationFile has already failed to find a .d.ts, since a
// package that ships real declarations is always preferred. Tries
// package.json's "module" field (ES module entry, when present — some
// packages ship both a CJS "main" and an ESM "module" build; the ESM
// one is more likely to use `export` syntax this scanner already
// handles well), then "main", then the bare-package Node default of
// "index.js" — matching entryDeclarationFile's own bounded, non-recursive,
// no bundler-`exports`-map-resolution scope exactly.
func entryJSFile(dir string) (string, bool) {
	var pkg packageJSONFields
	if data, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(data, &pkg)
	}

	candidates := []string{pkg.Module, pkg.Main, "index.js"}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		p := filepath.Join(dir, filepath.FromSlash(c))
		if fileExists(p) {
			return p, true
		}
		// package.json's "main" is commonly extensionless
		// ("./lib/index") — Node itself resolves that to "index.js"
		// next to it; mirror that one bounded case, no wider
		// resolution algorithm.
		if filepath.Ext(p) == "" {
			if withExt := p + ".js"; fileExists(withExt) {
				return withExt, true
			}
		}
	}
	return "", false
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}

// resolveEntry finds dir's best extraction entry point: a published
// .d.ts always wins when one exists (real declarations beat JSDoc
// comments — no type-checking needed, more complete signatures), the
// JSDoc-over-plain-JS fallback (NORM-008's deferred scope, closed here)
// only when none does. isJS tells the caller which extraction mode this
// is — extract.js itself doesn't need to know (its scanner already
// handles both syntaxes in one pass), but callers reporting ecosystem
// coverage/metrics might care.
func resolveEntry(dir string) (path string, isJS, ok bool) {
	if p, ok := entryDeclarationFile(dir); ok {
		return p, false, true
	}
	if p, ok := entryJSFile(dir); ok {
		return p, true, true
	}
	return "", false, false
}
