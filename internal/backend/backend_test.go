package backend_test

import (
	"context"
	"testing"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/backendtest"
)

func TestFakeBackendSatisfiesInterfaceAndFiltersByVersion(t *testing.T) {
	ctx := context.Background()
	b := backendtest.New()

	ns := backend.Namespace{Name: "depctl", Dimensions: 2, Distance: "cosine"}
	if err := b.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	req := backend.UpsertRequest{
		Namespace: ns.Name,
		Points: []backend.Point{
			{ID: "p1", Vector: []float32{1, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "grpc-go", Version: "v1.67.0"}},
			{ID: "p2", Vector: []float32{1, 0}, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "grpc-go", Version: "v1.68.0"}},
		},
	}
	if err := b.Upsert(ctx, req); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	result, err := b.Query(ctx, backend.QueryRequest{
		Namespace: ns.Name,
		Vector:    []float32{1, 0},
		TopK:      10,
		Filter:    &backend.Filter{Version: "v1.67.0"},
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Points) != 1 || result.Points[0].ID != "p1" {
		t.Errorf("Query result = %+v, want exactly p1", result.Points)
	}

	if err := b.Delete(ctx, backend.DeleteRequest{Namespace: ns.Name, IDs: []string{"p1"}}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	result, err = b.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: []float32{1, 0}, TopK: 10})
	if err != nil {
		t.Fatalf("Query after delete: %v", err)
	}
	if len(result.Points) != 1 || result.Points[0].ID != "p2" {
		t.Errorf("Query result after delete = %+v, want exactly p2", result.Points)
	}
}
