package cli

import (
	"context"
	"testing"

	bboltstore "github.com/aleutian-ai/depctl/internal/control/bbolt"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/planner"
)

// planVersionFixture is a real bbolt store holding one project whose
// recorded reference is for refVersion, with an active generation for
// activeVersion, and whose current resolution is resolvedVersion.
func planVersionFixture(t *testing.T, refVersion, activeVersion, resolvedVersion string) (*bboltstore.Store, string) {
	t.Helper()
	ctx := context.Background()
	store, _ := describeTestStores(t)
	const projectID = "proj_v"
	const pkg = "github.com/google/uuid"
	if err := store.PutProject(ctx, domain.Project{ID: projectID, Root: "/repo"}); err != nil {
		t.Fatalf("PutProject: %v", err)
	}
	if err := store.AddReference(ctx, domain.VersionReference{ProjectID: projectID, Ecosystem: domain.EcosystemGo, Package: pkg, Version: refVersion, Reason: domain.ReferenceReasonProject}); err != nil {
		t.Fatalf("AddReference: %v", err)
	}
	if activeVersion != "" {
		gen := domain.Generation{
			ID:         "gen_" + activeVersion,
			Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: pkg}, Version: activeVersion},
			State:      domain.GenReady,
		}
		if err := store.PutGeneration(ctx, gen); err != nil {
			t.Fatalf("PutGeneration: %v", err)
		}
		if err := store.PromoteGeneration(ctx, gen, "qdrant"); err != nil {
			t.Fatalf("PromoteGeneration: %v", err)
		}
	}
	res := domain.Resolution{Ecosystem: domain.EcosystemGo, Dependencies: []domain.DependencyVersion{{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: pkg, Direct: true}, Version: resolvedVersion,
	}}}
	if err := store.PutResolution(ctx, projectID, res); err != nil {
		t.Fatalf("PutResolution: %v", err)
	}
	return store, projectID
}

func plannedSyncVersions(t *testing.T, store *bboltstore.Store, projectID string) []string {
	t.Helper()
	plans, err := computePlans(context.Background(), store, "qdrant", projectID)
	if err != nil {
		t.Fatalf("computePlans: %v", err)
	}
	var out []string
	for _, p := range plans {
		for _, a := range p.Actions {
			if a.Kind == planner.ActionSyncVersion {
				out = append(out, a.Dependency.Version)
			}
		}
	}
	return out
}

// TestComputePlansVersionChangeBuildsNewVersion is VEC-016's live finding
// as a regression test, through the real plan.go caller: the project moves
// from v1.6.0 to v1.5.0 while v1.6.0 is active. Before PLAN-004 this
// planned nothing ("up to date").
func TestComputePlansVersionChangeBuildsNewVersion(t *testing.T) {
	store, projectID := planVersionFixture(t, "v1.6.0", "v1.6.0", "v1.5.0")
	if got := plannedSyncVersions(t, store, projectID); len(got) != 1 || got[0] != "v1.5.0" {
		t.Fatalf("planned SYNC_VERSION = %v, want exactly v1.5.0", got)
	}
}

// TestComputePlansReferencedButUnbuiltRetries is VEC-016's second finding
// through the real caller: reference recorded, build never succeeded.
func TestComputePlansReferencedButUnbuiltRetries(t *testing.T) {
	store, projectID := planVersionFixture(t, "v1.5.0", "", "v1.5.0")
	if got := plannedSyncVersions(t, store, projectID); len(got) != 1 || got[0] != "v1.5.0" {
		t.Fatalf("planned SYNC_VERSION = %v, want exactly v1.5.0", got)
	}
}

func TestComputePlansBuiltAndUnchangedPlansNothing(t *testing.T) {
	store, projectID := planVersionFixture(t, "v1.5.0", "v1.5.0", "v1.5.0")
	if got := plannedSyncVersions(t, store, projectID); len(got) != 0 {
		t.Fatalf("planned SYNC_VERSION = %v, want none", got)
	}
}

// TestComputePlansKnownNoSourceIsNotRetried: github.com/google/uuid has
// no registry manifest in this test's (built-in) registry, so a recorded
// no-source determination holds and nothing is retried.
func TestComputePlansKnownNoSourceIsNotRetried(t *testing.T) {
	store, projectID := planVersionFixture(t, "v1.5.0", "", "v1.5.0")
	if err := store.PutNoSourceVersion(context.Background(), bboltstore.NoSourceVersion{Ecosystem: domain.EcosystemGo, Package: "github.com/google/uuid", Version: "v1.5.0"}); err != nil {
		t.Fatalf("PutNoSourceVersion: %v", err)
	}
	if got := plannedSyncVersions(t, store, projectID); len(got) != 0 {
		t.Fatalf("planned SYNC_VERSION = %v, want none for a known no-source version", got)
	}
}

// promoteFixtureVersion makes dep's exact version genuinely built: an
// ACTIVE generation, promoted for backend. A fixture that seeds only a
// reference describes "referenced but never built" (which a plain sync
// retries, PLAN-005), not an up-to-date dependency.
func promoteFixtureVersion(t *testing.T, store *bboltstore.Store, dep domain.DependencyVersion, backend string) {
	t.Helper()
	gen := domain.Generation{ID: "gen_fixture_" + dep.Version, Dependency: dep, State: domain.GenReady}
	if err := store.PutGeneration(context.Background(), gen); err != nil {
		t.Fatalf("PutGeneration: %v", err)
	}
	if err := store.PromoteGeneration(context.Background(), gen, backend); err != nil {
		t.Fatalf("PromoteGeneration: %v", err)
	}
}
