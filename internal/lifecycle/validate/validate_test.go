package validate

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/data/generation"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/source/git"
)

func TestStructuralPassesForFullyValidGeneration(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())
	repoDir := newFixtureRepo(t, "v1.0.0")

	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	gen, manifest, replica := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)

	result, err := Structural(ctx, gen, manifest, replica, badgerStore)
	if err != nil {
		t.Fatalf("Structural: %v", err)
	}
	if !result.Passed {
		t.Errorf("Structural.Passed = false, failures = %v", result.Failures)
	}
	if len(result.Failures) != 0 {
		t.Errorf("Structural.Failures = %v, want empty", result.Failures)
	}
}

func TestStructuralFailsOnZeroChunkCount(t *testing.T) {
	ctx := context.Background()
	_, badgerStore := testStores(t)

	gen := domain.Generation{ID: "gen_empty", Dependency: testDependency("v1.0.0")}
	manifest := generation.Manifest{ID: gen.ID, ObjectCount: 3, ChunkCount: 0, Sources: []string{"repository"}}
	replica := domain.BackendReplica{GenerationID: gen.ID, PointCount: 0}

	result, err := Structural(ctx, gen, manifest, replica, badgerStore)
	if err != nil {
		t.Fatalf("Structural: %v", err)
	}
	if result.Passed {
		t.Fatal("Structural.Passed = true, want false (zero chunk count)")
	}
	found := false
	for _, f := range result.Failures {
		if f == "chunk count is zero" {
			found = true
		}
	}
	if !found {
		t.Errorf("Structural.Failures = %v, want to include \"chunk count is zero\"", result.Failures)
	}
}

func TestStructuralFailsOnPointCountMismatch(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())
	repoDir := newFixtureRepo(t, "v1.0.0")

	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	gen, manifest, replica := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)
	replica.PointCount = manifest.ChunkCount + 1 // simulate a mismatched replica

	result, err := Structural(ctx, gen, manifest, replica, badgerStore)
	if err != nil {
		t.Fatalf("Structural: %v", err)
	}
	if result.Passed {
		t.Fatal("Structural.Passed = true, want false (point count mismatch)")
	}
}

func TestStructuralFailsWhenManifestDoesNotMatchGeneration(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())
	repoDir := newFixtureRepo(t, "v1.0.0")

	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	gen, manifest, replica := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)
	manifest.ID = "gen_someone_else"

	result, err := Structural(ctx, gen, manifest, replica, badgerStore)
	if err != nil {
		t.Fatalf("Structural: %v", err)
	}
	if result.Passed {
		t.Fatal("Structural.Passed = true, want false (manifest/generation ID mismatch)")
	}
}
