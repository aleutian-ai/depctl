package keyword

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/conformance"
)

// TestKeywordConformance runs the shared VectorBackend suite (VEC-010)
// against a real file, through its text side.
func TestKeywordConformance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keyword.db")
	conformance.Run(t, func(t *testing.T) backend.VectorBackend { return New(path) })
}

// TestKeywordSearchFindsAPIDocs covers what keyword search is good at:
// identifiers and the words a doc actually uses. A paraphrase ("how do I
// get a connection from the pool" for Acquire) is where it degrades;
// that's measured against embeddings, not asserted here.
func TestKeywordSearchFindsAPIDocs(t *testing.T) {
	ctx := context.Background()
	s := New(filepath.Join(t.TempDir(), "keyword.db"))
	ns := backend.Namespace{Name: "ragctl-docs"}
	if err := s.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	meta := backend.PointMetadata{Ecosystem: "go", Dependency: "github.com/jackc/pgx/v5", Version: "v5.7.0", Generation: "g1"}
	docs := map[string]string{
		"newwithconfig": "func NewWithConfig(ctx context.Context, config *Config) (*Pool, error) creates a new Pool. config must have been created by ParseConfig.",
		"new":           "func New(ctx context.Context, connString string) (*Pool, error) creates a new Pool from a connection string. See ParseConfig.",
		"acquire":       "func (p *Pool) Acquire(ctx context.Context) (*Conn, error) returns a connection from the pool.",
		"readme":        "pgxpool is a concurrency-safe connection pool for pgx. Use it from multiple goroutines.",
	}
	var pts []backend.Point
	for id, text := range docs {
		pts = append(pts, backend.Point{ID: id, Text: text, Metadata: meta})
	}
	if err := s.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: pts}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	for q, want := range map[string]string{
		"pgxpool.NewWithConfig":       "newwithconfig",
		"NewWithConfig":               "newwithconfig",
		"connection string":           "new",
		"concurrency safe goroutines": "readme",
	} {
		res, err := s.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Text: q, TopK: 3, Filter: &backend.Filter{Dependency: meta.Dependency, Version: meta.Version}})
		if err != nil {
			t.Fatalf("Query(%q): %v", q, err)
		}
		if len(res.Points) == 0 || res.Points[0].ID != want {
			t.Errorf("Query(%q) top = %v, want %s first", q, ids(res.Points), want)
		}
	}
}

func ids(points []backend.ScoredPoint) []string {
	out := make([]string, len(points))
	for i, p := range points {
		out[i] = fmt.Sprintf("%s(%.2f)", p.ID, p.Score)
	}
	return out
}
