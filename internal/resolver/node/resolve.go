package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/resolver"
)

// Resolve picks a strategy by priority (package-lock.json, then
// pnpm-lock.yaml) and normalizes its exact dependency versions into a
// domain.Resolution. yarn.lock and bun.lock are recognized by
// lockfilePriority but have no resolution strategy yet.
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) {
	switch {
	case exists(root, "package-lock.json"):
		return resolveNpmLock(root)
	case exists(root, "pnpm-lock.yaml"):
		return resolvePnpmLock(root)
	case exists(root, "yarn.lock"), exists(root, "bun.lock"):
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf(
			"yarn.lock and bun.lock aren't resolvable yet — add a package-lock.json or pnpm-lock.yaml"))
	default:
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf(
			"no supported lockfile found (package.json alone, with no lockfile, isn't resolvable yet)"))
	}
}

func exists(root, name string) bool {
	info, err := os.Stat(lockPath(root, name))
	return err == nil && info.Mode().IsRegular()
}

func lockPath(root, name string) string {
	return filepath.Join(root, name)
}

func resolutionErr(root string, cause error) error {
	return &resolver.ResolutionError{Resolver: "node", Root: root, Cause: cause}
}
