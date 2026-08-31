// Package normalize converts acquired raw content (Markdown, plain text,
// Go source, release notes) into the domain's structured KnowledgeObject
// model. Every content-type-specific normalizer implements the shared
// Normalizer interface defined here.
package normalize

import (
	"context"

	"aleutian-ai/ragctl/internal/domain"
)

// Normalizer converts a materialized SourceSnapshot into zero or more
// KnowledgeObjects. Name()+Version() together are part of derived
// content identity (see HASH-001) — bumping Version() when parsing logic
// changes signals that previously normalized content needs
// re-normalization even though the underlying source bytes haven't
// changed.
type Normalizer interface {
	Name() string
	Version() string
	Supports(src domain.SourceSnapshot) bool
	Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error)
}

// Registry selects the first registered Normalizer whose Supports
// returns true, in registration order.
type Registry struct {
	normalizers []Normalizer
}

// NewRegistry returns a Registry trying normalizers in the given order.
func NewRegistry(normalizers ...Normalizer) *Registry {
	return &Registry{normalizers: normalizers}
}

// Select returns the first registered Normalizer that supports src, or
// (nil, false) if none do.
func (r *Registry) Select(src domain.SourceSnapshot) (Normalizer, bool) {
	for _, n := range r.normalizers {
		if n.Supports(src) {
			return n, true
		}
	}
	return nil, false
}
