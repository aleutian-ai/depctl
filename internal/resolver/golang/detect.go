// Package golang is ragctl's Go ecosystem resolver: detects Go module
// roots, resolves the module graph via the go toolchain, and normalizes it
// into the domain model. See docs/tickets/completed/06-go-resolver.
package golang

import (
	"context"
	"os"
	"path/filepath"
)

// Resolver implements resolver.Resolver for Go modules.
type Resolver struct{}

// New returns a Go ecosystem resolver.
func New() *Resolver { return &Resolver{} }

// Name returns "go".
func (r *Resolver) Name() string { return "go" }

// Detect reports whether root is a Go module root: a single os.Stat check
// for go.mod, no upward search, no content parsing.
func (r *Resolver) Detect(ctx context.Context, root string) (bool, error) {
	info, err := os.Stat(filepath.Join(root, "go.mod"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return info.Mode().IsRegular(), nil
}
