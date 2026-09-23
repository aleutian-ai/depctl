package symbolgraph

import (
	"context"
	"errors"
	"fmt"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/query"
)

// ErrDependencyNotResolved means a call site resolved to a real external
// symbol, but the project's own stored resolution doesn't have that
// module as a dependency — distinct from "not an external symbol"
// (SymbolProvider returning ok=false) and from "project not found"
// (ControlStore.GetResolution's own not-found error), so a caller can
// tell these three apart.
var ErrDependencyNotResolved = errors.New("symbolgraph: dependency not resolved for this project")

// ControlStore is the narrow slice of *bbolt.Store this package needs —
// just enough to map an ExternalSymbolRef to a project's already-
// resolved dependency version.
type ControlStore interface {
	GetResolution(ctx context.Context, projectID string) (domain.Resolution, error)
}

// QueryService is the narrow slice of *query.Service this package needs
// to turn a resolved dependency into an evidence bundle.
type QueryService interface {
	SearchKnowledge(ctx context.Context, q query.Query) (query.SearchResult, error)
}

// Resolver joins a project source call site to the exact-version
// knowledge evidence ragctl already has for the dependency it calls
// into (GRAPH-002) — "graph resolves symbol, ragctl resolves knowledge."
type Resolver struct {
	symbols SymbolProvider
	control ControlStore
	queries QueryService
}

// New returns a Resolver wired against the given provider/stores.
func New(symbols SymbolProvider, control ControlStore, queries QueryService) *Resolver {
	return &Resolver{symbols: symbols, control: control, queries: queries}
}

// EvidenceBundle is the end-to-end result of resolving one call site to
// the dependency-version evidence relevant to it.
type EvidenceBundle struct {
	Symbol     ExternalSymbolRef
	Dependency domain.DependencyVersion
	Result     query.SearchResult
}

// ResolveEvidence resolves site to its external symbol (via r.symbols),
// matches it against projectID's stored resolution, and searches that
// exact dependency+version for evidence relevant to queryText. An empty
// queryText defaults to the resolved symbol's own QualifiedName, since a
// caller (e.g. explain_call_site) may not have anything more specific to
// ask until the symbol itself is known. Returns (nil, nil) — not an
// error — if site does not resolve to an external symbol at all (an
// internal-to-project call site is not a failure, just nothing to
// join).
func (r *Resolver) ResolveEvidence(ctx context.Context, projectID string, site CallSite, queryText string) (*EvidenceBundle, error) {
	ref, ok, err := r.symbols.Resolve(ctx, site)
	if err != nil {
		return nil, fmt.Errorf("symbolgraph: resolve %s:%d:%d: %w", site.File, site.Line, site.Column, err)
	}
	if !ok {
		return nil, nil
	}
	if queryText == "" {
		queryText = ref.QualifiedName
	}

	resolution, err := r.control.GetResolution(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("symbolgraph: get resolution for project %s: %w", projectID, err)
	}

	matched, found := matchDependency(resolution, ref)
	if !found {
		return nil, fmt.Errorf("%w: %s (project %s)", ErrDependencyNotResolved, ref.Module, projectID)
	}

	result, err := r.queries.SearchKnowledge(ctx, query.Query{
		ProjectID:  projectID,
		Dependency: matched.Dependency.Name,
		Text:       queryText,
		Mode:       query.ModeProject,
	})
	if errors.Is(err, query.ErrNoActiveGeneration) {
		return nil, &NotSyncedError{Dependency: matched.Dependency.Name, Err: err}
	}
	if err != nil {
		return nil, err
	}

	return &EvidenceBundle{Symbol: ref, Dependency: matched, Result: result}, nil
}

// NotSyncedError reports that the dependency a call site resolved to has
// no synced knowledge yet, carrying its name so a caller can build it
// (SCOPE-004) instead of parsing an error message. It unwraps to
// query.ErrNoActiveGeneration, so existing errors.Is checks still match.
type NotSyncedError struct {
	Dependency string
	Err        error
}

func (e *NotSyncedError) Error() string {
	return fmt.Sprintf("dependency %s has not been synced yet: %v", e.Dependency, e.Err)
}

func (e *NotSyncedError) Unwrap() error { return e.Err }

// matchDependency finds the DependencyVersion in resolution whose name
// (and, when the symbol's ecosystem is known, ecosystem) matches ref —
// an exact match only, never a fuzzy/near-match guess.
func matchDependency(resolution domain.Resolution, ref ExternalSymbolRef) (domain.DependencyVersion, bool) {
	for _, dv := range resolution.Dependencies {
		if dv.Dependency.Name != ref.Module {
			continue
		}
		if ref.Ecosystem != "" && string(dv.Dependency.Ecosystem) != ref.Ecosystem {
			continue
		}
		return dv, true
	}
	return domain.DependencyVersion{}, false
}
