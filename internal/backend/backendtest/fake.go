// Package backendtest provides an in-memory backend.VectorBackend fake
// for tests — this package's own (VEC-001) and later ones (VAL-*, MCP-*)
// that need a VectorBackend without standing up a real vector database.
package backendtest

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"

	"aleutian-ai/ragctl/internal/backend"
)

// Backend is an in-memory backend.VectorBackend: one map of points per
// namespace, cosine-similarity search, no persistence.
type Backend struct {
	mu         sync.Mutex
	namespaces map[string]backend.Namespace
	points     map[string]map[string]backend.Point // namespace -> generation+chunk ID -> point (mirrors qdrant's pointID)
}

// New returns an empty Backend.
func New() *Backend {
	return &Backend{
		namespaces: map[string]backend.Namespace{},
		points:     map[string]map[string]backend.Point{},
	}
}

// Name identifies this fake backend.
func (b *Backend) Name() string { return "fake" }

// Capabilities reports full support — the fake exists to exercise every
// code path a real adapter might take.
func (b *Backend) Capabilities(ctx context.Context) (backend.Capabilities, error) {
	return backend.Capabilities{
		VectorSearch:   true,
		MetadataFilter: true,
		DeleteByFilter: true,
	}, nil
}

// EnsureNamespace creates ns if it doesn't already exist.
func (b *Backend) EnsureNamespace(ctx context.Context, ns backend.Namespace) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.namespaces[ns.Name]; !ok {
		b.namespaces[ns.Name] = ns
		b.points[ns.Name] = map[string]backend.Point{}
	}
	return nil
}

// Upsert writes or overwrites req.Points.
func (b *Backend) Upsert(ctx context.Context, req backend.UpsertRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	pts, ok := b.points[req.Namespace]
	if !ok {
		return fmt.Errorf("backendtest: namespace %q not found (call EnsureNamespace first)", req.Namespace)
	}
	for _, p := range req.Points {
		pts[p.Metadata.Generation+"\x00"+p.ID] = p
	}
	return nil
}

// Delete removes points by ID, by Filter, or both.
func (b *Backend) Delete(ctx context.Context, req backend.DeleteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	pts, ok := b.points[req.Namespace]
	if !ok {
		return nil
	}
	// IDs are chunk IDs; a chunk can be held by several generations, and
	// all of them go (mirrors qdrant's delete-by-_id-payload).
	for _, id := range req.IDs {
		for key, p := range pts {
			if p.ID == id {
				delete(pts, key)
			}
		}
	}
	if req.Filter != nil {
		for id, p := range pts {
			if matches(p.Metadata, req.Filter) {
				delete(pts, id)
			}
		}
	}
	return nil
}

// Query returns the TopK points nearest req.Vector by cosine similarity,
// constrained by req.Filter.
func (b *Backend) Query(ctx context.Context, req backend.QueryRequest) (backend.QueryResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	pts, ok := b.points[req.Namespace]
	if !ok {
		return backend.QueryResult{}, nil
	}

	var scored []backend.ScoredPoint
	for _, p := range pts {
		if req.Filter != nil && !matches(p.Metadata, req.Filter) {
			continue
		}
		scored = append(scored, backend.ScoredPoint{
			ID:       p.ID,
			Score:    cosineSimilarity(req.Vector, p.Vector),
			Metadata: p.Metadata,
		})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if req.TopK > 0 && len(scored) > req.TopK {
		scored = scored[:req.TopK]
	}
	return backend.QueryResult{Points: scored}, nil
}

// Health always succeeds — there's nothing external to be unreachable.
func (b *Backend) Health(ctx context.Context) error { return nil }

func matches(m backend.PointMetadata, f *backend.Filter) bool {
	if f.Ecosystem != "" && m.Ecosystem != f.Ecosystem {
		return false
	}
	if f.Dependency != "" && m.Dependency != f.Dependency {
		return false
	}
	if f.Version != "" && m.Version != f.Version {
		return false
	}
	if f.Generation != "" && m.Generation != f.Generation {
		return false
	}
	return true
}

func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(na) * math.Sqrt(nb)))
}

var _ backend.VectorBackend = (*Backend)(nil)
