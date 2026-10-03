package planner

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/registry"
)

func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.NewLoader("", "").Load(context.Background())
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	return reg
}

func grpcDep(version string) domain.DependencyVersion {
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "google.golang.org/grpc", Direct: true},
		Version:    version,
	}
}

func countByKind(actions []Action, kind ActionKind) int {
	n := 0
	for _, a := range actions {
		if a.Kind == kind {
			n++
		}
	}
	return n
}

func TestPlanNewDependencyProducesAddReferenceAndSyncVersion(t *testing.T) {
	ctx := context.Background()
	project := domain.Project{ID: "proj_1", Root: "/repo"}
	resolution := domain.Resolution{Ecosystem: domain.EcosystemGo, Dependencies: []domain.DependencyVersion{grpcDep("v1.67.0")}}

	actions, err := Plan(ctx, project, resolution, nil, testRegistry(t), map[string]bool{}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %+v, want exactly 2 (ADD_REFERENCE + SYNC_VERSION)", actions)
	}
	if countByKind(actions, ActionAddReference) != 1 {
		t.Errorf("want exactly 1 ADD_REFERENCE, got %+v", actions)
	}
	if countByKind(actions, ActionSyncVersion) != 1 {
		t.Errorf("want exactly 1 SYNC_VERSION, got %+v", actions)
	}
}

func TestPlanNewDependencySkipsSyncVersionWhenGenerationAlreadyActive(t *testing.T) {
	ctx := context.Background()
	project := domain.Project{ID: "proj_1", Root: "/repo"}
	dep := grpcDep("v1.67.0")
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{dep}}

	active := map[string]bool{GenerationKey(dep): true}
	actions, err := Plan(ctx, project, resolution, nil, testRegistry(t), active, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if countByKind(actions, ActionSyncVersion) != 0 {
		t.Errorf("want no SYNC_VERSION when a generation already exists, got %+v", actions)
	}
	if countByKind(actions, ActionAddReference) != 1 {
		t.Errorf("want ADD_REFERENCE regardless, got %+v", actions)
	}
}

func TestPlanVersionBumpProducesSyncAndDropWithGCCandidate(t *testing.T) {
	ctx := context.Background()
	project := domain.Project{ID: "proj_1", Root: "/repo"}
	current := []domain.VersionReference{{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.66.0"}}
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{grpcDep("v1.67.0")}}

	actions, err := Plan(ctx, project, resolution, current, testRegistry(t), map[string]bool{}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var sawSyncNew, sawDropOld, sawGCOld, sawAddNew bool
	for _, a := range actions {
		switch a.Kind {
		case ActionSyncVersion:
			if a.Dependency.Version == "v1.67.0" {
				sawSyncNew = true
			}
		case ActionDropReference:
			if a.Dependency.Version == "v1.66.0" {
				sawDropOld = true
			}
		case ActionGCCandidate:
			if a.Dependency.Version == "v1.66.0" {
				sawGCOld = true
			}
		case ActionAddReference:
			if a.Dependency.Version == "v1.67.0" {
				sawAddNew = true
			}
		}
	}
	if !sawSyncNew {
		t.Errorf("want SYNC_VERSION for v1.67.0, got %+v", actions)
	}
	if !sawDropOld {
		t.Errorf("want DROP_REFERENCE for v1.66.0, got %+v", actions)
	}
	if !sawGCOld {
		t.Errorf("want GC_CANDIDATE for v1.66.0, got %+v", actions)
	}
	// Without this, nothing ever marks the new version as referenced —
	// RET-001's grace-period/GC logic would eventually reap a project's
	// own current dependency version as orphaned.
	if !sawAddNew {
		t.Errorf("want ADD_REFERENCE for v1.67.0 (the project's new resolved version), got %+v", actions)
	}
}

func TestPlanUnchangedResolutionProducesOnlyNoop(t *testing.T) {
	ctx := context.Background()
	project := domain.Project{ID: "proj_1", Root: "/repo"}
	current := []domain.VersionReference{{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0"}}
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{grpcDep("v1.67.0")}}
	active := map[string]bool{GenerationKey(grpcDep("v1.67.0")): true}

	actions, err := Plan(ctx, project, resolution, current, testRegistry(t), active, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(actions) != 1 || actions[0].Kind != ActionNoop {
		t.Fatalf("actions = %+v, want exactly one NOOP", actions)
	}

	nonNoop := 0
	for _, a := range actions {
		if a.Kind != ActionNoop {
			nonNoop++
		}
	}
	if nonNoop != 0 {
		t.Errorf("got %d non-NOOP actions for an unchanged resolution, want 0", nonNoop)
	}
}

func TestPlanDependencyRemovedProducesDropReferenceAndGCCandidate(t *testing.T) {
	ctx := context.Background()
	project := domain.Project{ID: "proj_1", Root: "/repo"}
	current := []domain.VersionReference{{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0"}}
	resolution := domain.Resolution{Dependencies: nil} // grpc no longer in go.mod

	actions, err := Plan(ctx, project, resolution, current, testRegistry(t), map[string]bool{}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if countByKind(actions, ActionDropReference) != 1 {
		t.Errorf("want exactly 1 DROP_REFERENCE, got %+v", actions)
	}
	if countByKind(actions, ActionGCCandidate) != 1 {
		t.Errorf("want exactly 1 GC_CANDIDATE, got %+v", actions)
	}
}

func TestPlanUnmappedDependencyStillProducesActionWithReason(t *testing.T) {
	ctx := context.Background()
	project := domain.Project{ID: "proj_1", Root: "/repo"}
	dep := domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/totally-unknown-package"},
		Version:    "v0.1.0",
	}
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{dep}}

	actions, err := Plan(ctx, project, resolution, nil, testRegistry(t), map[string]bool{}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %+v, want 2 (unmapped package still plans, just flagged)", actions)
	}
	for _, a := range actions {
		if a.Reason != "no knowledge source mapped" {
			t.Errorf("action %+v: Reason = %q, want %q", a, a.Reason, "no knowledge source mapped")
		}
	}
}

func unknownDep(version string) domain.DependencyVersion {
	return domain.DependencyVersion{
		Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/totally-unknown-package"},
		Version:    version,
	}
}

func syncVersions(actions []Action) []string {
	var out []string
	for _, a := range actions {
		if a.Kind == ActionSyncVersion {
			out = append(out, a.Dependency.Version)
		}
	}
	return out
}

// TestPlanReferencedButUnbuiltRetries is PLAN-005's core case: the
// reference is unchanged, but this exact version was never built (an
// earlier build failed). It used to plan only a NOOP, forever.
func TestPlanReferencedButUnbuiltRetries(t *testing.T) {
	project := domain.Project{ID: "proj_1"}
	current := []domain.VersionReference{{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0"}}
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{grpcDep("v1.67.0")}}

	actions, err := Plan(context.Background(), project, resolution, current, testRegistry(t), map[string]bool{}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := syncVersions(actions); len(got) != 1 || got[0] != "v1.67.0" {
		t.Fatalf("SYNC_VERSION actions = %v, want exactly v1.67.0; all actions: %+v", got, actions)
	}
}

// TestPlanVersionChangeBuildsNewVersionWhileOldStaysActive is PLAN-004's
// planner-level case: another version being active must not suppress
// building the version the project now resolves.
func TestPlanVersionChangeBuildsNewVersionWhileOldStaysActive(t *testing.T) {
	project := domain.Project{ID: "proj_1"}
	current := []domain.VersionReference{{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.66.0"}}
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{grpcDep("v1.67.0")}}
	active := map[string]bool{GenerationKey(grpcDep("v1.66.0")): true}

	actions, err := Plan(context.Background(), project, resolution, current, testRegistry(t), active, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := syncVersions(actions); len(got) != 1 || got[0] != "v1.67.0" {
		t.Fatalf("SYNC_VERSION actions = %v, want exactly v1.67.0", got)
	}
}

// TestPlanKnownNoSourceUnmappedIsNotRetried: a version sync recorded as
// having no docs source, still unmapped in the registry, plans nothing.
func TestPlanKnownNoSourceUnmappedIsNotRetried(t *testing.T) {
	project := domain.Project{ID: "proj_1"}
	dep := unknownDep("v0.1.0")
	current := []domain.VersionReference{{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: dep.Dependency.Name, Version: "v0.1.0"}}
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{dep}}
	noSource := map[string]bool{GenerationKey(dep): true}

	actions, err := Plan(context.Background(), project, resolution, current, testRegistry(t), map[string]bool{}, noSource)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(actions) != 1 || actions[0].Kind != ActionNoop {
		t.Fatalf("actions = %+v, want exactly one NOOP", actions)
	}
}

// TestPlanKnownNoSourceButNowMappedIsRetried: if the registry has gained
// a manifest since the no-source record was made, the record is stale
// and the version is built.
func TestPlanKnownNoSourceButNowMappedIsRetried(t *testing.T) {
	project := domain.Project{ID: "proj_1"}
	current := []domain.VersionReference{{ProjectID: "proj_1", Ecosystem: domain.EcosystemGo, Package: "google.golang.org/grpc", Version: "v1.67.0"}}
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{grpcDep("v1.67.0")}}
	noSource := map[string]bool{GenerationKey(grpcDep("v1.67.0")): true}

	actions, err := Plan(context.Background(), project, resolution, current, testRegistry(t), map[string]bool{}, noSource)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := syncVersions(actions); len(got) != 1 {
		t.Fatalf("SYNC_VERSION actions = %v, want one (grpc is mapped in the built-in registry)", got)
	}
}

// TestPlanNewKnownNoSourceDependencyOnlyAddsReference: a first reference
// to a version already known to have no source records the reference
// but doesn't plan a build that's known to fail.
func TestPlanNewKnownNoSourceDependencyOnlyAddsReference(t *testing.T) {
	project := domain.Project{ID: "proj_1"}
	dep := unknownDep("v0.1.0")
	resolution := domain.Resolution{Dependencies: []domain.DependencyVersion{dep}}
	noSource := map[string]bool{GenerationKey(dep): true}

	actions, err := Plan(context.Background(), project, resolution, nil, testRegistry(t), map[string]bool{}, noSource)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if countByKind(actions, ActionAddReference) != 1 || countByKind(actions, ActionSyncVersion) != 0 {
		t.Fatalf("actions = %+v, want one ADD_REFERENCE and no SYNC_VERSION", actions)
	}
}
