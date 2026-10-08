package gc

import (
	"context"
	"testing"

	"github.com/aleutian-ai/depctl/internal/backend"
	"github.com/aleutian-ai/depctl/internal/backend/backendtest"
	"github.com/aleutian-ai/depctl/internal/domain"
	"github.com/aleutian-ai/depctl/internal/retention"
)

func testOrphanCandidate() retention.OrphanCandidate {
	return retention.OrphanCandidate{
		GenerationID: "gen_orphan",
		Ecosystem:    domain.EcosystemGo,
		Package:      "example.com/widget",
		Version:      "v1.0.0",
		State:        domain.GenFailed,
		Reason:       "failed",
	}
}

func TestRunOrphansDeletesGenerationScopedDataOnly(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	if err := vb.EnsureNamespace(ctx, ns); err != nil {
		t.Fatalf("EnsureNamespace: %v", err)
	}

	// Two generations sharing the same dependency+version — one orphaned
	// (never promoted), one healthy and active. This is the exact case
	// that would break if RunOrphans reused Run's dependency+version-
	// scoped deletion instead of generation-ID scoping.
	control.generations["gen_orphan"] = domain.Generation{
		ID:         "gen_orphan",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.0"},
		State:      domain.GenFailed,
	}
	control.generations["gen_active"] = domain.Generation{
		ID:         "gen_active",
		Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.0"},
		State:      domain.GenActive,
	}
	vec := []float32{1, 0, 0, 0}
	if err := vb.Upsert(ctx, backend.UpsertRequest{Namespace: ns.Name, Points: []backend.Point{
		{ID: "pt_orphan", Vector: vec, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "example.com/widget", Version: "v1.0.0", Generation: "gen_orphan"}},
		{ID: "pt_active", Vector: vec, Metadata: backend.PointMetadata{Ecosystem: "go", Dependency: "example.com/widget", Version: "v1.0.0", Generation: "gen_active"}},
	}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	data.deletedGenIDs = map[string]bool{} // gen_active's badger content is untouched by construction (only gen_orphan is in the plan)

	results, err := RunOrphans(ctx, control, data, vb, ns, []retention.OrphanCandidate{testOrphanCandidate()})
	if err != nil {
		t.Fatalf("RunOrphans: %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results = %+v, want one succeeded result", results)
	}

	// bbolt/Badger: only the orphan generation's records are gone.
	if !data.deletedGenIDs["gen_orphan"] {
		t.Error("Badger DeleteGeneration was never called for gen_orphan")
	}
	if data.deletedGenIDs["gen_active"] {
		t.Error("Badger DeleteGeneration was called for gen_active — must never touch the healthy generation")
	}
	if !control.deletedGenIDs["gen_orphan"] {
		t.Error("bbolt DeleteGenerationRecord was never called for gen_orphan")
	}
	if control.deletedGenIDs["gen_active"] {
		t.Error("bbolt DeleteGenerationRecord was called for gen_active — must never touch the healthy generation")
	}
	if len(control.deletedRefs) != 0 {
		t.Errorf("DeleteAllReferences was called (%v) — an orphan generation never owned reference rows", control.deletedRefs)
	}

	// Vector backend: only gen_orphan's point is gone, by Generation
	// filter alone — proving the deletion is generation-scoped, not
	// dependency+version-scoped (which would have deleted pt_active too).
	orphanPoints, err := vb.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: vec, TopK: 10, Filter: &backend.Filter{Generation: "gen_orphan"}})
	if err != nil {
		t.Fatalf("Query gen_orphan: %v", err)
	}
	if len(orphanPoints.Points) != 0 {
		t.Errorf("gen_orphan still has %d vector points after RunOrphans, want 0", len(orphanPoints.Points))
	}
	activePoints, err := vb.Query(ctx, backend.QueryRequest{Namespace: ns.Name, Vector: vec, TopK: 10, Filter: &backend.Filter{Generation: "gen_active"}})
	if err != nil {
		t.Fatalf("Query gen_active: %v", err)
	}
	if len(activePoints.Points) != 1 {
		t.Errorf("gen_active has %d vector points after RunOrphans, want 1 (untouched)", len(activePoints.Points))
	}
}

func TestRunOrphansFailureIsRestartable(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	data.failDelete = true // Badger delete fails on the first attempt
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	vb.EnsureNamespace(ctx, ns)
	control.generations["gen_orphan"] = domain.Generation{ID: "gen_orphan", Dependency: domain.DependencyVersion{Dependency: domain.Dependency{Ecosystem: domain.EcosystemGo, Name: "example.com/widget"}, Version: "v1.0.0"}}

	results, err := RunOrphans(ctx, control, data, vb, ns, []retention.OrphanCandidate{testOrphanCandidate()})
	if err != nil {
		t.Fatalf("RunOrphans: %v", err)
	}
	if len(results) != 1 || results[0].Succeeded {
		t.Fatalf("results = %+v, want one failed result", results)
	}
	if control.deletedGenIDs["gen_orphan"] {
		t.Error("bbolt record was deleted despite the earlier Badger-delete failure — ordering discipline broken")
	}

	job, err := control.GetJob(ctx, orphanJobID("gen_orphan"))
	if err != nil || job.State != domain.JobFailed {
		t.Fatalf("job = %+v, err = %v, want JobFailed", job, err)
	}

	// Re-run after "fixing" the failure: completes cleanly, resuming
	// from where it stopped.
	data.failDelete = false
	results, err = RunOrphans(ctx, control, data, vb, ns, []retention.OrphanCandidate{testOrphanCandidate()})
	if err != nil {
		t.Fatalf("RunOrphans (retry): %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results (retry) = %+v, want one succeeded result", results)
	}
	if !control.deletedGenIDs["gen_orphan"] {
		t.Error("bbolt record was never deleted after the retry")
	}
}

func TestRunOrphansAlreadySucceededJobIsANoOp(t *testing.T) {
	ctx := context.Background()
	control := newFakeControlStore()
	data := newFakeDataStore()
	vb := backendtest.New()
	ns := backend.Namespace{Name: "depctl", Dimensions: 4}
	vb.EnsureNamespace(ctx, ns)

	candidate := testOrphanCandidate()
	control.jobs[orphanJobID(candidate.GenerationID)] = domain.Job{ID: orphanJobID(candidate.GenerationID), State: domain.JobSucceeded}

	results, err := RunOrphans(ctx, control, data, vb, ns, []retention.OrphanCandidate{candidate})
	if err != nil {
		t.Fatalf("RunOrphans: %v", err)
	}
	if len(results) != 1 || !results[0].Succeeded {
		t.Fatalf("results = %+v, want one succeeded (no-op) result", results)
	}
	// No deletion calls should have been made — the job was already done.
	if len(data.deletedGenIDs) != 0 || len(control.deletedGenIDs) != 0 {
		t.Error("RunOrphans redid deletion work for an already-JobSucceeded candidate")
	}
}
