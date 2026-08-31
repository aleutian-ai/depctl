package python

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/resolver"
)

// Resolve picks a strategy by priority (uv.lock, then poetry.lock, then
// requirements.txt) and normalizes its exact dependency versions into a
// domain.Resolution. pyproject.toml alone (no lockfile) and Pipfile.lock
// are detected by Detect but have no resolution strategy yet.
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) {
	switch {
	case exists(root, "uv.lock"):
		return resolveUVLock(root)
	case exists(root, "poetry.lock"):
		return resolvePoetryLock(root)
	case exists(root, "requirements.txt"):
		return resolveRequirementsTxt(root)
	default:
		return domain.Resolution{}, resolutionErr(root, fmt.Errorf(
			"no supported lock/manifest file found (pyproject.toml alone and Pipfile.lock aren't resolvable yet — add a uv.lock, poetry.lock, or requirements.txt)"))
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
	return &resolver.ResolutionError{Resolver: "python", Root: root, Cause: cause}
}
