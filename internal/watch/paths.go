// Package watch detects changes to registered projects' dependency
// manifests, so `ragctl watch` re-resolves and syncs a project only when
// one of the files its resolver reads actually changes.
package watch

import (
	"os"
	"path/filepath"
	"slices"

	"aleutian-ai/ragctl/internal/domain"
)

// manifestFiles lists, per ecosystem, the files that ecosystem's resolver
// reads. Only ecosystems with a resolver are listed; add a line here when
// a new resolver lands.
var manifestFiles = map[domain.Ecosystem][]string{
	domain.EcosystemGo:     {"go.mod", "go.sum"},
	domain.EcosystemPython: {"pyproject.toml", "requirements.txt", "uv.lock", "poetry.lock", "Pipfile.lock"},
	domain.EcosystemNode:   {"package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock"},
}

// Project is one registered project to watch.
type Project struct {
	ID        string
	Root      string
	Ecosystem domain.Ecosystem
}

// WatchPaths returns the absolute paths of p's manifest files that exist
// right now. An ecosystem with no entry in manifestFiles yields nil.
func WatchPaths(p Project) []string {
	var paths []string
	for _, name := range manifestFiles[p.Ecosystem] {
		path := filepath.Join(p.Root, name)
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			paths = append(paths, path)
		}
	}
	return paths
}

func isManifest(eco domain.Ecosystem, name string) bool {
	return slices.Contains(manifestFiles[eco], name)
}
