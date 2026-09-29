package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/resolver"
)

// lockfileResolvers maps each name in lockfilePriority (detect.go) to the
// function that parses it — keeping the two coupled so a new lockfile
// format can't be added to one and forgotten in the other.
var lockfileResolvers = map[string]func(root string) (domain.Resolution, error){
	"package-lock.json": resolveNpmLock,
	"pnpm-lock.yaml":    resolvePnpmLock,
	"yarn.lock":         resolveYarnLock,
	"bun.lock":          resolveBunLock,
}

// Resolve picks a strategy by lockfilePriority's order and normalizes its
// exact dependency versions into a domain.Resolution.
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) {
	for _, name := range lockfilePriority {
		if exists(root, name) {
			return lockfileResolvers[name](root)
		}
	}
	return domain.Resolution{}, resolutionErr(root, fmt.Errorf(
		"no supported lockfile found (package.json alone, with no lockfile, isn't resolvable yet — "+
			"run npm install, pnpm install, yarn install, or bun install to generate one of "+
			"package-lock.json, pnpm-lock.yaml, yarn.lock, or bun.lock, then re-run `ragctl scan`)"))
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
