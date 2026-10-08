package promote

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/lifecycle/validate"
)

func openTestStore(t *testing.T) *bbolt.Store {
	t.Helper()
	s, err := bbolt.Open(filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatalf("bbolt.Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testGeneration(state domain.GenerationState) domain.Generation {
	return domain.Generation{
		ID: "gen_1",
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
			Version:    "v1.67.0",
		},
		State: state,
	}
}

func TestPromoteRejectsFailingValidation(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	candidate := testGeneration(domain.GenReady)
	if err := store.PutGeneration(ctx, candidate); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}

	passing := validate.StructuralResult{Passed: true}
	failing := validate.StructuralResult{Passed: false, Failures: []string{"chunk count is zero"}}

	err := Promote(ctx, store, candidate, "qdrant", passing, failing)
	if err == nil {
		t.Fatal("Promote succeeded, want error")
	}
	if !errors.Is(err, ErrValidationFailed) {
		t.Errorf("Promote error = %v, want wrapping ErrValidationFailed", err)
	}

	// No bbolt state should have been touched: generation record
	// unchanged, no active-generation pointer written.
	got, getErr := store.GetGeneration(ctx, candidate.ID)
	if getErr != nil {
		t.Fatalf("GetGeneration: %v", getErr)
	}
	if got.State != domain.GenReady {
		t.Errorf("generation.State = %s, want unchanged %s", got.State, domain.GenReady)
	}
	if _, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "qdrant"); !errors.Is(err, bbolt.ErrNotFound) {
		t.Errorf("GetActiveGeneration = %v, want ErrNotFound (nothing should have been promoted)", err)
	}
}

func TestPromoteRejectsNonReadyCandidate(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	candidate := testGeneration(domain.GenIndexing) // not READY yet
	if err := store.PutGeneration(ctx, candidate); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}

	err := Promote(ctx, store, candidate, "qdrant", validate.StructuralResult{Passed: true})
	if err == nil {
		t.Fatal("Promote succeeded, want error")
	}
	if !errors.Is(err, ErrNotReady) {
		t.Errorf("Promote error = %v, want wrapping ErrNotReady", err)
	}
}

// TestPromoteSucceedsAndSupersedesPrior: the prior is an earlier build of
// the same version (a rebuild). Under ADR-012 promotion supersedes only
// the same version's prior generation; other versions stay active.
func TestPromoteSucceedsAndSupersedesPrior(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	prior := testGeneration(domain.GenActive)
	prior.ID = "gen_prior"
	if err := store.PutGeneration(ctx, prior); err != nil {
		t.Fatalf("PutGeneration prior: %v", err)
	}
	if err := store.PromoteGeneration(ctx, prior, "qdrant"); err != nil {
		t.Fatalf("seed prior as active: %v", err)
	}

	candidate := testGeneration(domain.GenReady)
	if err := store.PutGeneration(ctx, candidate); err != nil {
		t.Fatalf("PutGeneration candidate: %v", err)
	}

	if err := Promote(ctx, store, candidate, "qdrant", validate.StructuralResult{Passed: true}); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	gotCandidate, err := store.GetGeneration(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("GetGeneration candidate: %v", err)
	}
	if gotCandidate.State != domain.GenActive {
		t.Errorf("candidate.State = %s, want %s", gotCandidate.State, domain.GenActive)
	}

	gotPrior, err := store.GetGeneration(ctx, prior.ID)
	if err != nil {
		t.Fatalf("GetGeneration prior: %v", err)
	}
	if gotPrior.State != domain.GenSuperseded {
		t.Errorf("prior.State = %s, want %s", gotPrior.State, domain.GenSuperseded)
	}

	active, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0", "qdrant")
	if err != nil {
		t.Fatalf("GetActiveGeneration: %v", err)
	}
	if active.ID != candidate.ID {
		t.Errorf("active.ID = %s, want %s", active.ID, candidate.ID)
	}
}
