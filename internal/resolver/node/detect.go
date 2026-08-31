// Package node is ragctl's Node.js/JavaScript/TypeScript ecosystem
// resolver: detects Node projects via package.json and resolves exact
// dependency versions from whichever lockfile is present. TypeScript rides
// the Node package graph, not a separate ecosystem. See
// docs/tickets/backlog/21-node-resolver.
package node

import (
	"context"
	"os"
	"path/filepath"
)

// lockfilePriority is the order Resolve tries lockfiles in when more than
// one is present.
var lockfilePriority = []string{
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
	"bun.lock",
}

// Resolver implements resolver.Resolver for Node.js projects.
type Resolver struct{}

// New returns a Node ecosystem resolver.
func New() *Resolver { return &Resolver{} }

func (r *Resolver) Name() string { return "node" }

// Detect reports whether root contains a package.json — the one signal
// that marks a directory as a Node project, independent of which (if any)
// lockfile is present.
func (r *Resolver) Detect(ctx context.Context, root string) (bool, error) {
	info, err := os.Stat(filepath.Join(root, "package.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
