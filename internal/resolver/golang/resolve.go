package golang

import (
	"context"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/resolver"
)

// Resolve runs `go list -m -json all` in root, normalizes the result, and
// returns a domain.Resolution with a stable fingerprint.
func (r *Resolver) Resolve(ctx context.Context, root string) (domain.Resolution, error) {
	modules, err := listModules(ctx, root)
	if err != nil {
		return domain.Resolution{}, err
	}

	deps, err := normalize(modules)
	if err != nil {
		return domain.Resolution{}, &resolver.ResolutionError{Resolver: "go", Root: root, Cause: err}
	}

	return domain.Resolution{
		Ecosystem:    domain.EcosystemGo,
		Dependencies: deps,
		Fingerprint:  resolver.Fingerprint(deps),
	}, nil
}
