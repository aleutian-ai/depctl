// Package resolver defines the contract every ecosystem-specific dependency
// resolver (Go, Python, Node) implements, plus a registry for deterministic
// resolver ordering. No ecosystem-specific logic lives here.
package resolver

import (
	"context"

	"github.com/aleutian-ai/depctl/internal/domain"
)

// Resolver detects whether a project root matches its ecosystem and, if so,
// resolves that project's dependencies.
type Resolver interface {
	Name() string
	Detect(ctx context.Context, root string) (bool, error)
	Resolve(ctx context.Context, root string) (domain.Resolution, error)
}

// Registry holds resolvers in a fixed, deterministic priority order —
// registration order is the priority, no dynamic scoring.
type Registry struct {
	resolvers []Resolver
}

// NewRegistry builds a Registry from resolvers, preserving argument order as
// the registration/priority order.
func NewRegistry(resolvers ...Resolver) *Registry {
	return &Registry{resolvers: resolvers}
}

// DetectAll returns every registered resolver whose Detect returned true for
// root, in registration order. A project can match more than one resolver
// (e.g. a polyglot repo) — the caller runs each independently.
func (r *Registry) DetectAll(ctx context.Context, root string) ([]Resolver, error) {
	var matched []Resolver
	for _, res := range r.resolvers {
		ok, err := res.Detect(ctx, root)
		if err != nil {
			return nil, err
		}
		if ok {
			matched = append(matched, res)
		}
	}
	return matched, nil
}
