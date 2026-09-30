// Package project detects local project roots by walking a directory tree
// for known ecosystem manifest files, and assigns stable IDs to them. See
// docs/tickets/completed/04-project-discovery.
package project

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"aleutian-ai/ragctl/internal/domain"
)

// DetectedProject is one (root, ecosystem) match found by Scan. A polyglot
// directory (e.g. go.mod and package.json both present) produces multiple
// DetectedProject entries sharing the same Root.
type DetectedProject struct {
	Root      string
	Ecosystem domain.Ecosystem
}

// skipDirs are never descended into, by exact directory-name match.
var skipDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	"target":       true,
	".venv":        true,
	"venv":         true,
	"__pycache__":  true,
}

// markers maps a manifest filename to the ecosystem it indicates. Detection
// only checks for the marker file's existence — no content parsing.
var markers = map[string]domain.Ecosystem{
	"go.mod":           domain.EcosystemGo,
	"pyproject.toml":   domain.EcosystemPython,
	"requirements.txt": domain.EcosystemPython,
	"package.json":     domain.EcosystemNode,
	"Cargo.toml":       domain.EcosystemRust,
	"pom.xml":          domain.EcosystemJava,
	"build.gradle":     domain.EcosystemJava,
	"build.gradle.kts": domain.EcosystemJava,
}

// Scan walks root and returns every detected project. Permission errors on
// a subdirectory skip that subtree and continue scanning the rest of the
// tree; a non-existent or non-directory root is an immediate error.
func Scan(ctx context.Context, root string) ([]DetectedProject, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("scan root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root %s: not a directory", root)
	}

	var results []DetectedProject
	seen := map[string]bool{}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			// Permission-denied (or similar) on a subdirectory: skip that
			// subtree and keep going rather than aborting the whole scan.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && skipDirs[d.Name()] {
			return filepath.SkipDir
		}

		canonical, err := canonicalize(path)
		if err != nil {
			return nil
		}

		for marker, eco := range markers {
			if _, statErr := os.Stat(filepath.Join(path, marker)); statErr == nil {
				key := canonical + "|" + string(eco)
				if !seen[key] {
					seen[key] = true
					results = append(results, DetectedProject{Root: canonical, Ecosystem: eco})
				}
			}
		}
		return nil
	})
	if err != nil {
		return results, err
	}

	return results, nil
}

// canonicalize resolves path to the same string regardless of how it was
// spelled, so the project ID it feeds into stays stable across every
// call path (PROJ-002). filepath.Abs alone isn't enough: it's a no-op
// Clean for an already-absolute path (preserving a symlink component
// verbatim, e.g. macOS's /tmp -> /private/tmp), but resolves a relative
// path via os.Getwd(), which falls back to the kernel's getcwd() (always
// fully symlink-resolved) whenever $PWD is unset or stale — exactly the
// case for a daemon/MCP-spawned subprocess. `ragctl scan /tmp/foo`
// (literal, absolute) and ragctl serve's own startup scan (".", relative,
// from a working directory under /tmp) used to diverge into two
// different canonical roots — and therefore two different project IDs —
// for the identical physical directory. EvalSymlinks closes that gap by
// always resolving to the same real path either way. If it fails (e.g. a
// dangling symlink, or a permissions error resolving an ancestor), that's
// treated as non-fatal: better to canonicalize as well as Abs+Clean
// managed than to fail the scan entirely over a display-path oddity.
func canonicalize(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}
