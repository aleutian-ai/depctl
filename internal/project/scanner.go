// Package project detects local project roots by walking a directory tree
// for known ecosystem manifest files, and assigns stable IDs to them. See
// docs/tickets/planned/04-project-discovery.
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

func canonicalize(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
