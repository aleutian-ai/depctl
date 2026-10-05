package embedded

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/conformance"
)

// TestEmbeddedConformance runs the shared VectorBackend suite (VEC-010)
// against a real file. Every subtest shares one file, as every ragctl
// namespace does; the suite gives each its own namespace.
func TestEmbeddedConformance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	conformance.Run(t, func(t *testing.T) backend.VectorBackend { return New(path) })
}

func TestEmbeddedSpecifics(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "vectors.db")
	s := New(path)
	ns := backend.Namespace{Name: "ragctl-specifics", Dimensions: 4, Distance: "cosine"}
	if err := s.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	meta := backend.PointMetadata{Ecosystem: "go", Dependency: "d", Version: "v1", Generation: "g"}

	t.Run("ReEnsuringWithADifferentDimensionIsAClearError", func(t *testing.T) {
		bad := ns
		bad.Dimensions = 768
		if err := s.EnsureNamespace(ctx, bad); !errors.Is(err, ErrDimensionMismatch) {
			t.Fatalf("EnsureNamespace with a new dimension = %v, want ErrDimensionMismatch", err)
		}
	})

	t.Run("WrongSizedVectorWritesNothing", func(t *testing.T) {
		ok := backend.Point{ID: "ok", Vector: []float32{1, 0, 0, 0}, Metadata: meta}
		bad := backend.Point{ID: "bad", Vector: []float32{1, 0}, Metadata: meta}
		if err := s.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{ok, bad}}); !errors.Is(err, ErrDimensionMismatch) {
			t.Fatalf("Upsert with a 2-dim vector = %v, want ErrDimensionMismatch", err)
		}
		if n, err := s.Count(ctx, ns.Name, nil); err != nil || n != 0 {
			t.Errorf("Count after the failed batch = %d (err %v), want 0: the whole batch must roll back", n, err)
		}
		if _, err := s.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: []float32{1, 0}}); !errors.Is(err, ErrDimensionMismatch) {
			t.Errorf("Query with a 2-dim vector = %v, want ErrDimensionMismatch", err)
		}
	})

	t.Run("UnsupportedDistanceIsRejected", func(t *testing.T) {
		if err := s.EnsureNamespace(ctx, backend.Namespace{Name: "ragctl-dot", Dimensions: 4, Distance: "dot"}); err == nil {
			t.Fatal("EnsureNamespace with distance \"dot\" succeeded, want an error")
		}
	})

	// The daemon is the only writer (ADR-011), but it writes from several
	// goroutines (sync, GC); bbolt serializes them, so nothing is lost.
	t.Run("ConcurrentWritersAllLand", func(t *testing.T) {
		cns := backend.Namespace{Name: "ragctl-concurrent", Dimensions: 4, Distance: "cosine"}
		if err := s.EnsureNamespace(ctx, cns); err != nil {
			t.Fatalf("EnsureNamespace: %v", err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for w := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				var pts []backend.Point
				for i := range 50 {
					pts = append(pts, backend.Point{ID: fmt.Sprintf("w%d-%d", w, i), Vector: []float32{1, float32(i), 0, 0}, Metadata: meta})
				}
				errs <- New(path).Upsert(ctx, backend.UpsertRequest{Namespace: cns.Name, Points: pts})
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent Upsert: %v", err)
			}
		}
		if n, err := s.Count(ctx, cns.Name, nil); err != nil || n != 400 {
			t.Errorf("Count after 8 writers x 50 points = %d (err %v), want 400", n, err)
		}
	})
}

// Another process holding the file (normally the daemon) must be a clear
// error, not a hang.
func TestFileHeldElsewhereIsAClearError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vectors.db")
	holder, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("bolt.Open: %v", err)
	}
	defer holder.Close()
	err = New(path).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "in use by another ragctl process") {
		t.Fatalf("Health on a locked file = %v, want an in-use error", err)
	}
}

// BenchmarkQueryOneVersion measures a realistic search: 768 dimensions,
// 50,000 points in the file, one dependency version of 2,000 chunks
// selected by the filter.
func BenchmarkQueryOneVersion(b *testing.B) {
	ctx := context.Background()
	s := New(filepath.Join(b.TempDir(), "vectors.db"))
	ns := backend.Namespace{Name: "bench", Dimensions: 768, Distance: "cosine"}
	if err := s.EnsureNamespace(ctx, ns); err != nil {
		b.Fatal(err)
	}
	vec := func(seed int) []float32 {
		v := make([]float32, 768)
		for i := range v {
			v[i] = float32((seed*31+i*17)%97) / 97
		}
		return v
	}
	for dep := range 25 {
		var pts []backend.Point
		for i := range 2000 {
			pts = append(pts, backend.Point{ID: fmt.Sprintf("c%d", i), Vector: vec(dep*2000 + i),
				Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: fmt.Sprintf("dep%d", dep), Version: "v1", Generation: fmt.Sprintf("g%d", dep)}})
		}
		if err := s.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: pts}); err != nil {
			b.Fatal(err)
		}
	}
	req := backend.QueryRequest{Namespace: ns.Name, Vector: vec(7), TopK: 10, Filter: &backend.Filter{Ecosystem: "go", Dependency: "dep12", Version: "v1", Generation: "g12"}}
	for b.Loop() {
		if _, err := s.Query(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}
