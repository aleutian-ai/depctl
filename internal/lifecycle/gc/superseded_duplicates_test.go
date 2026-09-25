package gc

import (
	"context"
	"testing"

	"aleutian-ai/ragctl/internal/backend"
	"aleutian-ai/ragctl/internal/backend/backendtest"
	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/retention"
)

func testSupersededDuplicateCandidate() retention.SupersededDuplicateCandidate {
	return retention.SupersededDuplicateCandidate{
		GenerationID: "gen_dup",
		Ecosystem:    domain.EcosystemGo,
		Package:      "example.com/widget",
		Version:      "v1.0.0",
	}
}

// TestRunSupersededDuplicatesDeletesGenerationScopedDataOnly is
// POINT-004's core deletion regression: two generations sharing the
// identical (ecosystem, package, version) — one ACTIVE, one SUPERSEDED
// — must have only the SUPERSEDED one's points/data removed. A
// dependency+version-scoped delete (like Run's own vector step) would
// wrongly take the ACTIVE generation's identical-version points with
// it; this proves the Generation-only filter keeps them apart.
func TestRunSupersededDuplicatesDeletesGenerationScopedDataOnly(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	if err := vb.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	control.generations["gen_dup"] = domain.Generation{
		ID:         "gen_dup",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.0"},
		State:      domain.GenSuperseded,
	}
	control.generations["gen_active"] = domain.Generation{
		ID:         "gen_active",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.0"},
		State:      domain.GenActive,
	}
	vec := []float32{1, 0, 0, 0}
	if err := vb.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{
		{ID: "pt_dup", Vector: vec, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "example.com/widget", Version: "v1.0.0", Generation: "gen_dup"}},
		{ID: "pt_active", Vector: vec, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "example.com/widget", Version: "v1.0.0", Generation: "gen_active"}},
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	results, err := RunSupersededDuplicates(ctx, control, data, vb, ns, []retention.SupersededDuplicateCandidate{testSupersededDuplicateCandidate()})
	if err != nil {
		t.Fatalf("RunSupersededDuplicates: %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results = %+v, want one succeeded result", results)
	}

	if !data.deletedGenIDs["gen_dup"] {
		t.Error("Badger DeleteGeneration was never called for gen_dup")
	}
	if data.deletedGenIDs["gen_active"] {
		t.Error("Badger DeleteGeneration was called for gen_active — must never touch the currently active generation")
	}
	if !control.deletedGenIDs["gen_dup"] {
		t.Error("bbolt DeleteGenerationRecord was never called for gen_dup")
	}
	if control.deletedGenIDs["gen_active"] {
		t.Error("bbolt DeleteGenerationRecord was called for gen_active — must never touch the currently active generation")
	}
	if len(control.deletedRefs) != 0 {
		t.Errorf("DeleteAllReferences was called (%v) — a superseded duplicate never owned reference rows (the active generation does)", control.deletedRefs)
	}

	dupPoints, err := vb.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: vec, TopK: 10, Filter: &backend.Filter{Generation: "gen_dup"}})
	if err != nil {
		t.Fatalf("Query gen_dup: %v", err)
	}
	if len(dupPoints.Points) != 0 {
		t.Errorf("gen_dup still has %d vector points after RunSupersededDuplicates, want 0", len(dupPoints.Points))
	}
	activePoints, err := vb.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: vec, TopK: 10, Filter: &backend.Filter{Generation: "gen_active"}})
	if err != nil {
		t.Fatalf("Query gen_active: %v", err)
	}
	if len(activePoints.Points) != 1 {
		t.Errorf("gen_active has %d vector points after RunSupersededDuplicates, want 1 (untouched)", len(activePoints.Points))
	}
}

func TestRunSupersededDuplicatesFailureIsRestartable(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	data.failDelete = true
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	vb.EnsureNamespace(ctx, ns)
	control.generations["gen_dup"] = domain.Generation{ID: "gen_dup", Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.0"}}

	results, err := RunSupersededDuplicates(ctx, control, data, vb, ns, []retention.SupersededDuplicateCandidate{testSupersededDuplicateCandidate()})
	if err != nil {
		t.Fatalf("RunSupersededDuplicates: %v", err)
	}
	if len(results) != 1 || results[0].Succeeded {
		t.Fatalf("results = %+v, want one failed result", results)
	}
	if control.deletedGenIDs["gen_dup"] {
		t.Error("bbolt record was deleted despite the earlier Badger-delete failure — ordering discipline broken")
	}

	job, err := control.GetJob(ctx, supersededDuplicateJobID("gen_dup"))
	if err != nil || job.State != domain.JobFailed {
		t.Fatalf("job = %+v, err = %v, want JobFailed", job, err)
	}

	data.failDelete = false
	results, err = RunSupersededDuplicates(ctx, control, data, vb, ns, []retention.SupersededDuplicateCandidate{testSupersededDuplicateCandidate()})
	if err != nil {
		t.Fatalf("RunSupersededDuplicates (retry): %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results (retry) = %+v, want one succeeded result", results)
	}
	if !control.deletedGenIDs["gen_dup"] {
		t.Error("bbolt record was never deleted after the retry")
	}
}

func TestRunSupersededDuplicatesAlreadySucceededJobIsANoOp(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	vb := backendtest.New()
	ns := backend.Namespace{Name: "ragctl", Dimensions: 4}
	vb.EnsureNamespace(ctx, ns)

	candidate := testSupersededDuplicateCandidate()
	control.jobs[supersededDuplicateJobID(candidate.GenerationID)] = domain.Job{ID: supersededDuplicateJobID(candidate.GenerationID), State: domain.JobSucceeded}

	results, err := RunSupersededDuplicates(ctx, control, data, vb, ns, []retention.SupersededDuplicateCandidate{candidate})
	if err != nil {
		t.Fatalf("RunSupersededDuplicates: %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results = %+v, want one succeeded (no-op) result", results)
	}
	if len(data.deletedGenIDs) != 0 || len(control.deletedGenIDs) != 0 {
		t.Error("RunSupersededDuplicates redid deletion work for an already-JobSucceeded candidate")
	}
}
