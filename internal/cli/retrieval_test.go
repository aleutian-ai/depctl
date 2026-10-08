package cli

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/backendtest"
	"github.com/aleutian-ai/depctl/internal/backend/keyword"
)

// searchIndexFixture: generation "both" is in the vector store and the
// keyword index; "kwonly" was built while the embedder was unavailable,
// so it's only in the keyword index.
func searchIndexFixture(t *testing.T) (*searchIndex, backend.Namespace) {
	t.Helper()
	ctx := context.Background()
	idx := &searchIndex{name: "embedded", vector: backendtest.New(), keyword: keyword.New(filepath.Join(t.TempDir(), "keyword.db"))}
	ns := backend.Namespace{Name: "depctl-test", Dimensions: 2, Distance: "cosine"}
	if err := idx.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	meta := func(gen string) backend.PointMetadata {
		return backend.PointMetadata{Ecosystem: "go", Dependency: "d", Version: gen, Generation: gen}
	}
	both := []backend.Point{{ID: "a", Vector: []float32{1, 0}, Text: "NewRandom returns a random UUID", Metadata: meta("both")}}
	if err := idx.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: both}); err != nil {
		t.Fatalf("Upsert both: %v", err)
	}
	kwOnly := []backend.Point{{ID: "b", Text: "NewRandom returns a random UUID", Metadata: meta("kwonly")}}
	if err := idx.keyword.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: kwOnly}); err != nil {
		t.Fatalf("Upsert kwonly: %v", err)
	}
	return idx, ns
}

// With a query vector and both indexes (auto mode, embedder ready), the
// result is the two rankings fused: a doc both rank well comes first,
// and a doc only one of them finds still appears.
func TestSearchIndexFusesKeywordAndVectorRankings(t *testing.T) {
	ctx := context.Background()
	idx := &searchIndex{name: "embedded", vector: backendtest.New(), keyword: keyword.New(filepath.Join(t.TempDir(), "keyword.db"))}
	ns := backend.Namespace{Name: "depctl-test", Dimensions: 2, Distance: "cosine"}
	if err := idx.EnsureNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	m := backend.PointMetadata{Ecosystem: "go", Dependency: "d", Version: "v1", Generation: "g"}
	points := []backend.Point{
		{ID: "both", Vector: []float32{0.9, 0.1}, Text: "random uuid random uuid", Metadata: m},                  // keyword #1, vector #2
		{ID: "keyword-only", Vector: []float32{0, 1}, Text: "random uuid plus several other words", Metadata: m}, // keyword #2, vector #3
		{ID: "vector-only", Vector: []float32{1, 0}, Text: "nothing in common", Metadata: m},                     // vector #1 only
	}
	if err := idx.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: points}); err != nil {
		t.Fatal(err)
	}
	res, err := idx.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: []float32{1, 0}, Text: "random uuid", TopK: 3, Filter: &backend.Filter{Generation: "g"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range res.Points {
		got = append(got, p.ID)
	}
	if want := []string{"both", "keyword-only", "vector-only"}; !reflect.DeepEqual(got, want) {
		t.Errorf("fused ranking = %v, want %v", got, want)
	}
}

func TestSearchIndexFallsBackToKeywordForAGenerationWithoutVectors(t *testing.T) {
	idx, ns := searchIndexFixture(t)
	res, err := idx.Query(context.Background(), backend.QueryRequest{Namespace: ns.Name, Vector: []float32{1, 0}, Text: "NewRandom", Filter: &backend.Filter{Generation: "kwonly"}})
	if err != nil || len(res.Points) != 1 || res.Points[0].ID != "b" {
		t.Fatalf("Query = %+v, %v; want point b found by keyword", res.Points, err)
	}
}

func TestSearchIndexUsesKeywordWithoutAQueryVector(t *testing.T) {
	idx, ns := searchIndexFixture(t)
	res, err := idx.Query(context.Background(), backend.QueryRequest{Namespace: ns.Name, Text: "NewRandom", Filter: &backend.Filter{Generation: "both"}})
	if err != nil || len(res.Points) != 1 || res.Points[0].ID != "a" {
		t.Fatalf("Query = %+v, %v; want point a found by keyword", res.Points, err)
	}
}

// A generation is present if any index holds it.
func TestSearchIndexCountIsTheLargestAcrossIndexes(t *testing.T) {
	idx, ns := searchIndexFixture(t)
	ctx := context.Background()
	for gen, want := range map[string]int{"both": 1, "kwonly": 1, "missing": 0} {
		if n, err := idx.Count(ctx, ns.Name, &backend.Filter{Generation: gen}); err != nil || n != want {
			t.Errorf("Count(%s) = %d, %v; want %d", gen, n, err, want)
		}
	}
	// A vector-only index (an install switched to auto from vector mode)
	// with an empty keyword index still counts its generations.
	vecOnly := backendtest.New()
	if err := vecOnly.EnsureNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if err := vecOnly.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "c", Vector: []float32{1, 0}, Metadata: backend.PointMetadata{Generation: "old"}}}}); err != nil {
		t.Fatal(err)
	}
	switched := &searchIndex{name: "embedded", vector: vecOnly, keyword: keyword.New(filepath.Join(t.TempDir(), "keyword.db"))}
	if n, err := switched.Count(ctx, ns.Name, &backend.Filter{Generation: "old"}); err != nil || n != 1 {
		t.Errorf("Count with an empty keyword index = %d, %v; want 1 from the vector store", n, err)
	}
}

// Before anything is embedded, the vector store has no namespace at all;
// that must not make keyword-only generations look empty or broken.
func TestSearchIndexCountWithAVectorStoreThatHasNoNamespaceYet(t *testing.T) {
	ctx := context.Background()
	kw := keyword.New(filepath.Join(t.TempDir(), "keyword.db"))
	ns := backend.Namespace{Name: "depctl-test"}
	if err := kw.EnsureNamespace(ctx, ns); err != nil {
		t.Fatal(err)
	}
	if err := kw.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{{ID: "a", Text: "NewRandom", Metadata: backend.PointMetadata{Generation: "g"}}}}); err != nil {
		t.Fatal(err)
	}
	idx := &searchIndex{name: "embedded", vector: backendtest.New(), keyword: kw} // vector namespace never created
	if n, err := idx.Count(ctx, ns.Name, &backend.Filter{Generation: "g"}); err != nil || n != 1 {
		t.Errorf("Count = %d, %v; want 1 from the keyword index", n, err)
	}
}
