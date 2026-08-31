package bbolt

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
)

func testGeneration(id, version string, state domain.GenerationState) domain.Generation {
	return domain.Generation{
		ID: id,
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc"},
			Version:    version,
		},
		State: state,
	}
}

func TestGetActiveGenerationNotFound(t *testing.T) {
	store := openTestStore(t)
	_, err := store.GetActiveGeneration(context.Background(), domain.EcosystemGo, "google.golang.org/grpc", "qdrant")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveGeneration = %v, want ErrNotFound", err)
	}
}

func TestPromoteGenerationWithNoPriorJustActivates(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	gen := testGeneration("gen_1", "v1.67.0", domain.GenReady)
	if err := store.PutGeneration(ctx, gen); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}

	if err := store.PromoteGeneration(ctx, gen, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}

	got, err := store.GetGeneration(ctx, gen.ID)
	if err != nil {
		t.Fatalf("GetGeneration: %v", err)
	}
	if got.State != domain.GenActive {
		t.Errorf("gen.State = %s, want %s", got.State, domain.GenActive)
	}

	active, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "google.golang.org/grpc", "qdrant")
	if err != nil {
		t.Fatalf("GetActiveGeneration: %v", err)
	}
	if active.ID != gen.ID {
		t.Errorf("active.ID = %s, want %s", active.ID, gen.ID)
	}
}

func TestPromoteGenerationSupersedesPrior(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	prior := testGeneration("gen_prior", "v1.66.0", domain.GenActive)
	if err := store.PutGeneration(ctx, prior); err != nil {
		t.Fatalf("PutGeneration prior: %v", err)
	}
	if err := store.PromoteGeneration(ctx, prior, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration prior: %v", err)
	}

	candidate := testGeneration("gen_candidate", "v1.67.0", domain.GenReady)
	if err := store.PutGeneration(ctx, candidate); err != nil {
		t.Fatalf("PutGeneration candidate: %v", err)
	}
	if err := store.PromoteGeneration(ctx, candidate, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration candidate: %v", err)
	}

	gotPrior, err := store.GetGeneration(ctx, prior.ID)
	if err != nil {
		t.Fatalf("GetGeneration prior: %v", err)
	}
	if gotPrior.State != domain.GenSuperseded {
		t.Errorf("prior.State = %s, want %s", gotPrior.State, domain.GenSuperseded)
	}

	gotCandidate, err := store.GetGeneration(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("GetGeneration candidate: %v", err)
	}
	if gotCandidate.State != domain.GenActive {
		t.Errorf("candidate.State = %s, want %s", gotCandidate.State, domain.GenActive)
	}

	active, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "google.golang.org/grpc", "qdrant")
	if err != nil {
		t.Fatalf("GetActiveGeneration: %v", err)
	}
	if active.ID != candidate.ID {
		t.Errorf("active.ID = %s, want %s (candidate)", active.ID, candidate.ID)
	}
}

func TestPromoteGenerationPersistsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "control.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	gen := testGeneration("gen_1", "v1.67.0", domain.GenReady)
	if err := s.PutGeneration(ctx, gen); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}
	if err := s.PromoteGeneration(ctx, gen, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	active, err := s2.GetActiveGeneration(ctx, domain.EcosystemGo, "google.golang.org/grpc", "qdrant")
	if err != nil {
		t.Fatalf("GetActiveGeneration after reopen: %v", err)
	}
	if active.ID != gen.ID || active.State != domain.GenActive {
		t.Errorf("active generation after reopen = %+v", active)
	}
}

func TestActiveGenerationsScopedByBackend(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	genQdrant := testGeneration("gen_qdrant", "v1.67.0", domain.GenReady)
	if err := store.PutGeneration(ctx, genQdrant); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}
	if err := store.PromoteGeneration(ctx, genQdrant, "qdrant"); err != nil {
		t.Fatalf("PromoteGeneration qdrant: %v", err)
	}

	if _, err := store.GetActiveGeneration(ctx, domain.EcosystemGo, "google.golang.org/grpc", "other-backend"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetActiveGeneration(other-backend) = %v, want ErrNotFound (promotion is backend-scoped)", err)
	}
}
