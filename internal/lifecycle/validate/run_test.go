package validate

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/source/git"
)

// TestRunThenPromoteEndToEnd is the vertical-slice proof epics 11-14
// exist for: a real Build, a real Replicate, Run's three checks all
// passing, and Promote actually flipping the bbolt active pointer — no
// simulated intermediate state anywhere in the chain.
func TestRunThenPromoteEndToEnd(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t, "v1.0.0")
	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	gen, manifest, replica := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)

	gen, report, err := Run(ctx, gen, manifest, replica, nil, nil, DefaultSanityConfig(), embedder, vb, ns, store, badgerStore)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.Passed() {
		t.Fatalf("report did not pass: %v", report.Failures())
	}
	if gen.State != domain.GenReady {
		t.Fatalf("gen.State = %s, want %s", gen.State, domain.GenReady)
	}

	// internal/lifecycle/promote imports this package (to accept
	// validate.StructuralResult), so a test here can't import promote
	// back without an import cycle — promote.Promote's own precondition
	// checks (failing validation, non-READY candidate) are covered by
	// internal/lifecycle/promote's own tests instead. This calls the
	// same underlying bbolt primitive Promote calls once its checks pass.
	if err := store.PromoteGeneration(ctx, gen, vb.Name()); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}

	active, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "example.com/widget", "v1.0.0", vb.Name())
	if err != nil {
		t.Fatalf("GetActiveGeneration: %v", err)
	}
	if active.ID != gen.ID {
		t.Errorf("active.ID = %s, want %s", active.ID, gen.ID)
	}
	if active.State != domain.GenActive {
		t.Errorf("active.State = %s, want %s", active.State, domain.GenActive)
	}
}

// TestRunFailsWhenReplicaIsIncomplete proves the negative path: a
// generation whose replica is missing points never reaches READY, so
// Promote (checked separately) would refuse it.
func TestRunFailsWhenReplicaIsIncomplete(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t, "v1.0.0")
	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	gen, manifest, replica := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)
	replica.PointCount = 0 // simulate an interrupted/incomplete replication

	gen, report, err := Run(ctx, gen, manifest, replica, nil, nil, DefaultSanityConfig(), embedder, vb, ns, store, badgerStore)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Passed() {
		t.Fatal("report passed, want a structural failure (point count mismatch)")
	}
	if gen.State != domain.GenFailed {
		t.Errorf("gen.State = %s, want %s", gen.State, domain.GenFailed)
	}
	if gen.Error == "" {
		t.Error("gen.Error is empty, want the failure reasons recorded")
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.State != domain.GenFailed {
		t.Errorf("persisted gen.State = %s, want %s", got.State, domain.GenFailed)
	}
}

// TestRunSanityBlocksImplausibleCollapse proves VAL-002 actually blocks
// promotion when a later generation's counts collapse relative to the
// prior active one, using two real generations.
func TestRunSanityBlocksImplausibleCollapse(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	store, badgerStore := testStores(t)
	gitCache := git.NewCache(t.TempDir())

	repoDir := newFixtureRepo(t, "v1.0.0")
	embedder := &fakeEmbedder{dims: 4}
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}

	priorGen, priorManifest, _ := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)

	// Second version whose object count is reported (via a hand-edited
	// manifest) as having collapsed — Sanity should catch this
	// regardless of what actually happened during Build.
	candGen, candManifest, candReplica := buildAndReplicate(t, ctx, store, badgerStore, gitCache, repoDir, testDependency("v1.0.0"), embedder, vb, ns)
	candManifest.ObjectCount = 1 // simulate an implausible collapse from prior's real count

	candGen, report, err := Run(ctx, candGen, candManifest, candReplica, &priorGen, &priorManifest, DefaultSanityConfig(), embedder, vb, ns, store, badgerStore)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Sanity.Passed {
		t.Fatal("Sanity passed, want a floor-ratio failure")
	}
	if report.Passed() {
		t.Fatal("report.Passed() = true, want false (Sanity should block promotion)")
	}
	if candGen.State != domain.GenFailed {
		t.Errorf("candidate.State = %s, want %s", candGen.State, domain.GenFailed)
	}
}
