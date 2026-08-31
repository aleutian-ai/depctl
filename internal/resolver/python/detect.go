// Package python is ragctl's Python ecosystem resolver: detects Python
// projects via their lock/manifest files and resolves exact dependency
// versions, preferring lockfiles (uv, then Poetry) over unpinned
// manifests. See docs/tickets/backlog/20-python-resolver.
package python

import (
	"context"
	"os"
	"path/filepath"
)

// recognizedFiles are the lock/manifest files that mark a directory as a
// Python project, in resolution priority order (uv.lock first).
var recognizedFiles = []string{
	"uv.lock",
	"poetry.lock",
	"requirements.txt",
	"pyproject.toml",
	"Pipfile.lock",
}

// Resolver implements resolver.Resolver for Python projects.
type Resolver struct{}

// New returns a Python ecosystem resolver.
func New() *Resolver { return &Resolver{} }

func (r *Resolver) Name() string { return "python" }

// Detect reports whether root contains any recognized Python lock or
// manifest file — a file-existence check only, no parsing.
func (r *Resolver) Detect(ctx context.Context, root string) (bool, error) {
	for _, name := range recognizedFiles {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return false, err
		}
		if info.Mode().IsRegular() {
			return true, nil
		}
	}
	return false, nil
}
