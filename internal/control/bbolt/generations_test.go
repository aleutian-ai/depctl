package bbolt

import (
	"context"
	"errors"
	"testing"

	"github.com/aleutian-ai/depctl/internal/domain"
)

func testGen(id, pkg, version string) domain.Generation {
	return domain.Generation{
		ID: id,
		Dependency: domain.DependencyVersion{
			Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: pkg},
			Version:    version,
		},
	}
}

func TestListGenerationsByDependencyVersion(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	gens := []domain.Generation{
		testGen("gen_1", "google.golang.org/grpc", "v1.67.0"),
		testGen("gen_2", "google.golang.org/grpc", "v1.67.0"), // rebuilt generation, same version
		testGen("gen_3", "google.golang.org/grpc", "v1.68.0"),
		testGen("gen_4", "github.com/pkg/errors", "v0.9.1"),
	}
	for _, g := range gens {
		if err := store.PutGeneration(ctx, g); err != nil {
			t.Fatalf("PutGeneration: %v", err)
		}
	}

	got, err := store.ListGenerationsByDependencyVersion(ctx, domain.EcosystemGo, "google.golang.org/grpc", "v1.67.0")
	if err != nil {
		t.Fatalf("ListGenerationsByDependencyVersion: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d generations, want 2 (gen_1 and gen_2)", len(got))
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids["gen_1"] || !ids["gen_2"] {
		t.Errorf("got = %+v, want gen_1 and gen_2", got)
	}
}

func TestListAllGenerations(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	gens := []domain.Generation{
		testGen("gen_1", "google.golang.org/grpc", "v1.67.0"),
		testGen("gen_2", "github.com/pkg/errors", "v0.9.1"),
		testGen("gen_3", "example.com/foo", "v1.0.0"),
	}
	for _, g := range gens {
		if err := store.PutGeneration(ctx, g); err != nil {
			t.Fatalf("PutGeneration: %v", err)
		}
	}

	got, err := store.ListAllGenerations(ctx)
	if err != nil {
		t.Fatalf("ListAllGenerations: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d generations, want 3", len(got))
	}
	ids := map[string]bool{}
	for _, g := range got {
		ids[g.ID] = true
	}
	if !ids["gen_1"] || !ids["gen_2"] || !ids["gen_3"] {
		t.Errorf("got = %+v, want gen_1, gen_2, gen_3", got)
	}
}

func TestDeleteGenerationRecord(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	g := testGen("gen_1", "google.golang.org/grpc", "v1.67.0")
	if err := store.PutGeneration(ctx, g); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}
	if err := store.DeleteGenerationRecord(ctx, "gen_1"); err != nil {
		t.Fatalf("DeleteGenerationRecord: %v", err)
	}
	if _, err := store.GetGeneration(ctx, "gen_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetGeneration after delete = %v, want ErrNotFound", err)
	}

	// Deleting an already-absent record is not an error (GC idempotency).
	if err := store.DeleteGenerationRecord(ctx, "gen_1"); err != nil {
		t.Errorf("DeleteGenerationRecord (already absent): %v", err)
	}
}
