// Package normalize converts acquired raw content (Markdown, plain text,
// Go source, release notes) into the domain's structured KnowledgeObject
// model. Every content-type-specific normalizer implements the shared
// Normalizer interface defined here.
//
// SEC-004 invariant: no normalizer, in this package or any of its
// subpackages, ever executes code from the dependency content it reads.
// godoc/Go extraction uses go/parser's static AST, never `go run`/`go
// build` on the checked-out source. pydoc and tsdoc shell out to a real
// `python3`/`node` interpreter, but only to run ragctl's own extraction
// script (embedded in the binary, piped over stdin) — the dependency's
// file path is passed as a plain string argument for that script to
// statically parse (Python's ast module; a hand-written CommonJS/ESM
// text scanner for JS/TS), never imported, required, or eval'd as live
// code. See pydoc/tsdoc's own runExtract functions for the exact
// mechanism.
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
