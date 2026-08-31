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

	actions, err := Plan(ctx, project, resolution, nil, testRegistry(t), map[string]bool{})
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
	actions, err := Plan(ctx, project, resolution, nil, testRegistry(t), active)
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

	actions, err := Plan(ctx, project, resolution, current, testRegistry(t), map[string]bool{})
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

	actions, err := Plan(ctx, project, resolution, current, testRegistry(t), map[string]bool{})
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

	actions, err := Plan(ctx, project, resolution, current, testRegistry(t), map[string]bool{})
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

	actions, err := Plan(ctx, project, resolution, nil, testRegistry(t), map[string]bool{})
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
