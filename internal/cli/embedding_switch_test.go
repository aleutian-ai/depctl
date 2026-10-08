package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/backendtest"
	"github.com/aleutian-ai/depctl/internal/backend/embedded"
	"github.com/aleutian-ai/depctl/internal/config"
	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/domain"
)

// switchFixture is an install with two active generations embedded by
// "old-model" at 4 dimensions and one built without vectors, configured
// now for "new-model". vb holds the two generations' points plus one
// from another install sharing the store.
func switchFixture(t *testing.T, vb backend.VectorBackend) (*bboltstore.Store, config.Config, backend.Namespace, []string) {
	t.Helper()
	ctx := context.Background()
	store, badgerStore, _, _ := statusTestStores(t)
	cfg := config.Default(t.TempDir())
	cfg.Vector.Backend = "qdrant" // seedActiveGeneration's backend name
	cfg.Embedding.Model, cfg.Embedding.QueryPrompt, cfg.Embedding.DocumentPrompt, cfg.Embedding.Dimensions = "new-model", "", "", 0

	ns := backend.Namespace{Name: "depctl-test", Dimensions: 4, Distance: "cosine"}
	if err := vb.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}
	var gens []string
	for _, pkg := range []string{"pkg-a", "pkg-b", "pkg-keyword-only"} {
		gen := seedActiveGeneration(t, store, badgerStore, domain.EcosystemNode, pkg, "1.0.0", 1)
		gens = append(gens, gen)
		if pkg == "pkg-keyword-only" {
			continue
		}
		replica, err := store.GetBackendReplica(ctx, gen, cfg.Vector.Backend)
		if err != nil {
			t.Fatalf("GetBackendReplica: %v", err)
		}
		replica.EmbeddingModel, replica.Dimensions = "old-model", 4
		if err := store.PutBackendReplica(ctx, replica); err != nil {
			t.Fatalf("PutBackendReplica: %v", err)
		}
	}
	points := []backend.Point{
		{ID: "a", Vector: []float32{1, 0, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "node", Dependency: "pkg-a", Version: "1.0.0", Generation: gens[0]}},
		{ID: "b", Vector: []float32{0, 1, 0, 0}, Metadata: backend.PointMetadata{Ecosystem: "node", Dependency: "pkg-b", Version: "1.0.0", Generation: gens[1]}},
		{ID: "x", Vector: []float32{0, 0, 1, 0}, Metadata: backend.PointMetadata{Ecosystem: "node", Dependency: "other", Version: "1.0.0", Generation: "gen-of-another-install"}},
	}
	if err := vb.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: points}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	return store, cfg, ns, gens
}

func assertReplicasCleared(t *testing.T, store *bboltstore.Store, cfg config.Config, gens []string) {
	t.Helper()
	for _, gen := range gens {
		r, err := store.GetBackendReplica(context.Background(), gen, cfg.Vector.Backend)
		if err != nil {
			t.Fatalf("GetBackendReplica: %v", err)
		}
		if r.EmbeddingModel != "" || r.Dimensions != 0 {
			t.Errorf("replica %s = %q at %d dims, want cleared so backfill re-embeds it", gen, r.EmbeddingModel, r.Dimensions)
		}
	}
}

// The embedded store is this install's alone, so a switch drops its
// namespace, which may then take another size.
func TestSwitchEmbeddingDropsTheEmbeddedNamespace(t *testing.T) {
	ctx := context.Background()
	vb := embedded.New(filepath.Join(t.TempDir(), "vectors.db"))
	store, cfg, ns, gens := switchFixture(t, vb)
	if !hasStaleVectors(ctx, store, cfg) {
		t.Fatal("hasStaleVectors = false with replicas from another model")
	}

	ns.Dimensions = 2
	var out bytes.Buffer
	if err := switchEmbedding(ctx, store, cfg, vb, ns, &out); err != nil {
		t.Fatalf("switchEmbedding: %v", err)
	}
	if !strings.Contains(out.String(), "re-embedding 2 version(s)") {
		t.Errorf("output = %q, want it to say 2 versions are re-embedded", out.String())
	}
	assertReplicasCleared(t, store, cfg, gens[:2])
	if hasStaleVectors(ctx, store, cfg) {
		t.Error("hasStaleVectors = true after the switch")
	}
	if n, err := vb.Count(ctx, ns.Name, nil); err != nil || n != 0 {
		t.Errorf("Count after the switch = %d (err %v), want 0", n, err)
	}
	p := backend.Point{ID: "a", Vector: []float32{1, 0}, Metadata: backend.PointMetadata{Ecosystem: "node", Dependency: "pkg-a", Version: "1.0.0", Generation: gens[0]}}
	if err := vb.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{p}}); err != nil {
		t.Errorf("Upsert at the new size: %v", err)
	}
}

// A remote collection may be shared, so a switch at the same size deletes
// only this install's points.
func TestSwitchEmbeddingDeletesOnlyThisInstallsRemotePoints(t *testing.T) {
	ctx := context.Background()
	vb := backendtest.New()
	store, cfg, ns, gens := switchFixture(t, vb)

	if err := switchEmbedding(ctx, store, cfg, vb, ns, &bytes.Buffer{}); err != nil {
		t.Fatalf("switchEmbedding: %v", err)
	}
	assertReplicasCleared(t, store, cfg, gens[:2])
	if n, err := vb.Count(ctx, ns.Name, nil); err != nil || n != 1 {
		t.Errorf("Count after the switch = %d (err %v), want 1: the other install's point", n, err)
	}
}

// A remote collection has one size: if it still holds this install's
// vectors at the old size, nothing changes and the error says what to do.
// In a new, empty collection the switch goes ahead.
func TestSwitchEmbeddingNeedsANewRemoteCollectionForANewSize(t *testing.T) {
	ctx := context.Background()
	vb := backendtest.New()
	store, cfg, ns, gens := switchFixture(t, vb)

	ns.Dimensions = 2
	err := switchEmbedding(ctx, store, cfg, vb, ns, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "set vector.collection to a new name") {
		t.Fatalf("switchEmbedding at a new size = %v, want the new-collection guidance", err)
	}
	if !hasStaleVectors(ctx, store, cfg) {
		t.Error("replicas changed although the switch refused")
	}
	if n, _ := vb.Count(ctx, ns.Name, nil); n != 3 {
		t.Errorf("Count = %d, want all 3 points kept", n)
	}

	ns.Name = "depctl-new"
	if err := switchEmbedding(ctx, store, cfg, vb, ns, &bytes.Buffer{}); err != nil {
		t.Fatalf("switchEmbedding into a new collection: %v", err)
	}
	assertReplicasCleared(t, store, cfg, gens[:2])
}
